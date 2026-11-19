package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

// Artifacts on the runner: after a step, each artifact it keeps is copied
// out of the workspace and uploaded in chunks; before a step, each artifact
// it fetches is downloaded, checked against its SHA-256, and put in the
// workspace. Keeping never changes a step's outcome; a fetch that fails
// fails the step (or, when the server cannot be reached, hands it back to
// be run again).

// ---- client ----

// ErrChunkOffset says the server has a different amount of a file than the
// chunk assumed; Received is where to carry on.
type chunkOffset struct{ Received int64 }

func (e *chunkOffset) Error() string { return fmt.Sprintf("the server has %d bytes", e.Received) }

// BeginArtifact declares an artifact.
func (c *Client) BeginArtifact(ctx context.Context, lease string, in engine.BeginArtifact) (*engine.BegunArtifact, error) {
	var out engine.BegunArtifact
	if _, err := c.call(ctx, "artifacts/begin", map[string]any{"lease_id": lease, "artifact": in}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CommitArtifact marks an artifact whose files are all sent as kept.
func (c *Client) CommitArtifact(ctx context.Context, lease, id string) error {
	_, err := c.call(ctx, "artifacts/commit", map[string]any{"lease_id": lease, "artifact_id": id}, nil)
	return err
}

// PutChunk sends one chunk of a file, returning how many bytes of it the
// server has.
func (c *Client) PutChunk(ctx context.Context, lease, artifact, filePath string, offset int64, data []byte) (int64, error) {
	sum := sha256.Sum256(data)
	q := url.Values{"lease_id": {lease}, "artifact_id": {artifact}, "path": {filePath}, "offset": {strconv.FormatInt(offset, 10)},
		"sha256": {hex.EncodeToString(sum[:])}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.Server+"/api/runner/v1/artifacts/chunk?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized {
		c.contact.Store(time.Now().UnixMilli())
	}
	var reply struct {
		Received int64  `json:"received"`
		Error    string `json:"error"`
	}
	_ = json.Unmarshal(body, &reply)
	switch {
	case resp.StatusCode == http.StatusOK:
		return reply.Received, nil
	case resp.StatusCode == http.StatusGone:
		return 0, ErrLeaseLost
	case resp.StatusCode == http.StatusConflict && strings.Contains(reply.Error, "send the chunk that starts there"):
		return 0, &chunkOffset{Received: reply.Received}
	}
	if reply.Error == "" {
		reply.Error = strings.TrimSpace(string(body))
	}
	return 0, &httpError{code: resp.StatusCode, msg: fmt.Sprintf("artifacts/chunk: %s (HTTP %d)", reply.Error, resp.StatusCode)}
}

// httpError is an answer the server gave with an error status.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

// Download streams a file of an artifact a step fetches, from offset.
func (c *Client) Download(ctx context.Context, lease, artifact, filePath string, offset int64) (io.ReadCloser, error) {
	body, _ := json.Marshal(map[string]any{"lease_id": lease, "artifact_id": artifact, "path": filePath, "offset": offset})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/runner/v1/artifacts/download", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	// The client's timeout covers the whole body; a large file takes longer.
	hc := *c.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized {
		c.contact.Store(time.Now().UnixMilli())
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &e)
	if resp.StatusCode == http.StatusGone {
		return nil, ErrLeaseLost
	}
	return nil, &httpError{code: resp.StatusCode, msg: fmt.Sprintf("artifacts/download: %s (HTTP %d)", nonEmpty(e.Error, strings.TrimSpace(string(data))), resp.StatusCode)}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// permanent reports whether an error from the server will not go away by
// asking again: a 4xx answer other than a timeout or too many requests.
func permanent(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.code >= 400 && he.code < 500 && he.code != http.StatusRequestTimeout && he.code != http.StatusTooManyRequests
	}
	return errors.Is(err, ErrLeaseLost)
}

// retry calls fn up to 5 times while it fails with errors that may go away,
// waiting a little longer each time.
func retry(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = fn(); err == nil || permanent(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<i) * 500 * time.Millisecond):
		}
	}
	return err
}

// ---- keeping ----

// keepTimeout bounds how long keeping one step's artifacts may take.
const keepTimeout = 30 * time.Minute

// keepArtifacts copies out and uploads the artifacts a step keeps after an
// attempt that ended with outcome. What could not be kept is said in the
// step's log.
func (r *Runner) keepArtifacts(job *engine.Job, spec executor.Spec, outcome string, sys func(string, ...any)) {
	ja := job.Artifacts
	if ja == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()
	var used int64
	for _, a := range ja.Keep {
		if !a.KeepAfter(outcome) {
			continue
		}
		label := "Artifact " + a.Name
		if a.Name == "sbom" {
			label = "SBOM"
		}
		if ja.Problem != "" {
			sys("%s: not kept: %s", label, ja.Problem)
			continue
		}
		dir := filepath.Join(r.DataDir, "artifacts", job.LeaseID, a.Name)
		lim := executor.StageLimits{MaxFiles: engine.MaxArtifactFiles, MaxFileBytes: ja.MaxFileBytes, MaxBytes: max(ja.MaxBytes-used, 1)}
		staged, err := executor.Stage(ctx, r.execFor(job), spec, a.Paths, lim, dir)
		if staged != nil {
			for _, n := range staged.Notes {
				sys("%s: %s", label, spec.Mask(n))
			}
		}
		if err != nil {
			sys("%s: not kept: %s", label, spec.Mask(strings.TrimPrefix(err.Error(), executor.ErrNotKept.Error()+": ")))
			os.RemoveAll(dir)
			continue
		}
		size, err := r.upload(ctx, job, a.Name, staged)
		os.RemoveAll(dir)
		if err != nil {
			sys("%s: not kept: %s", label, spec.Mask(err.Error()))
			continue
		}
		used += size
		sys("%s: kept %s (%s)", label, filesText(len(staged.Files)), sizeText(size))
	}
	os.RemoveAll(filepath.Join(r.DataDir, "artifacts", job.LeaseID))
}

func filesText(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// upload sends a staged artifact: declare it, send what the server lacks
// chunk by chunk, carrying on from what it has after a failure, and commit.
func (r *Runner) upload(ctx context.Context, job *engine.Job, name string, staged *executor.Staged) (int64, error) {
	in := engine.BeginArtifact{Name: name}
	byPath := map[string]executor.StagedFile{}
	var total int64
	for _, f := range staged.Files {
		in.Files = append(in.Files, engine.ArtifactFileInfo{Path: f.Path, Size: f.Size, SHA256: f.SHA256, Mode: f.Mode})
		byPath[f.Path] = f
		total += f.Size
	}
	for round := 0; round < 3; round++ {
		var begun *engine.BegunArtifact
		err := retry(ctx, func() error {
			var err error
			begun, err = r.Client.BeginArtifact(ctx, job.LeaseID, in)
			return err
		})
		if err != nil {
			return 0, err
		}
		for _, bf := range begun.Files {
			if bf.Done {
				continue
			}
			f, ok := byPath[bf.Path]
			if !ok {
				return 0, fmt.Errorf("the server named a file that is not in the artifact: %s", bf.Path)
			}
			if err := r.sendFile(ctx, job, begun, f, bf.Received); err != nil {
				return 0, err
			}
		}
		err = retry(ctx, func() error { return r.Client.CommitArtifact(ctx, job.LeaseID, begun.ID) })
		if err == nil {
			return total, nil
		}
		var he *httpError
		if !errors.As(err, &he) || he.code != http.StatusConflict {
			return 0, err
		}
		// Some content went away before the commit: declare it again,
		// which says what to send.
	}
	return 0, errors.New("the server kept asking for files again")
}

// sendFile sends one file from offset, in chunks.
func (r *Runner) sendFile(ctx context.Context, job *engine.Job, begun *engine.BegunArtifact, f executor.StagedFile, offset int64) error {
	chunk := begun.ChunkBytes
	if chunk <= 0 {
		chunk = engine.ArtifactChunkBytes
	}
	src, err := os.Open(f.Local)
	if err != nil {
		return err
	}
	defer src.Close()
	buf := make([]byte, chunk)
	restarts := 0
	for offset < f.Size {
		offset -= offset % chunk
		n, err := src.ReadAt(buf[:min(chunk, f.Size-offset)], offset)
		if err != nil && !(errors.Is(err, io.EOF) && int64(n) == min(chunk, f.Size-offset)) {
			return err
		}
		var got int64
		err = retry(ctx, func() error {
			var err error
			got, err = r.Client.PutChunk(ctx, job.LeaseID, begun.ID, f.Path, offset, buf[:n])
			var co *chunkOffset
			if errors.As(err, &co) {
				got, err = co.Received, nil
			}
			return err
		})
		if err != nil {
			var he *httpError
			if errors.As(err, &he) && he.code == http.StatusBadRequest && strings.Contains(he.msg, "checksum mismatch") && restarts < 2 {
				restarts++
				offset = 0
				continue
			}
			return err
		}
		if got <= offset && got < f.Size {
			// The server lost progress (another server restarted the
			// upload): carry on from what it has.
			restarts++
			if restarts > 5 {
				return fmt.Errorf("sending %s made no progress", f.Path)
			}
		}
		offset = got
	}
	return nil
}

// ---- fetching ----

// fetchTimeout bounds how long fetching one step's artifacts may take.
const fetchTimeout = 30 * time.Minute

// fetch puts the artifacts a step fetches into its workspace. It returns
// the result to report instead of running the step if that cannot be done.
func (r *Runner) fetch(ctx context.Context, job *engine.Job, spec executor.Spec, sys func(string, ...any), out executor.Sink) *engine.Result {
	if len(job.Fetch) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	dir := filepath.Join(r.DataDir, "fetch", job.LeaseID)
	defer os.RemoveAll(dir)
	var placed []executor.PlacedFile
	n := 0
	for _, f := range job.Fetch {
		label := fmt.Sprintf("Artifact %s of %s", f.Name, f.From)
		if f.Problem != "" {
			sys("%s cannot be fetched: %s", label, f.Problem)
			return &engine.Result{Outcome: "failed", Error: fmt.Sprintf("fetching artifact %s of %s: %s", f.Name, f.From, f.Problem)}
		}
		var size int64
		for _, fl := range f.Files {
			n++
			local := filepath.Join(dir, strconv.Itoa(n))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return &engine.Result{Outcome: "lost", Error: "preparing to fetch artifacts: " + err.Error()}
			}
			if err := r.downloadFile(ctx, job, f.ArtifactID, fl, local); err != nil {
				msg := fmt.Sprintf("fetching %s of artifact %s of %s: %v", fl.Path, f.Name, f.From, err)
				sys("%s", msg)
				if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
					return &engine.Result{Outcome: "canceled"}
				}
				if permanent(err) {
					return &engine.Result{Outcome: "failed", Error: msg}
				}
				return &engine.Result{Outcome: "lost", Error: msg}
			}
			target := fl.Path
			if f.Into != "" {
				target = path.Join(f.Into, fl.Path)
			}
			placed = append(placed, executor.PlacedFile{Path: target, Local: local, Mode: fl.Mode})
			size += fl.Size
		}
		into := "the workspace"
		if f.Into != "" {
			into = f.Into + "/"
		}
		sys("%s: fetched %s (%s) into %s", label, filesText(len(f.Files)), sizeText(size), into)
	}
	if err := executor.Place(ctx, r.Exec, spec, placed, out); err != nil {
		msg := "putting fetched artifacts in the workspace: " + spec.Mask(err.Error())
		sys("%s", msg)
		if errors.Is(err, executor.ErrInfrastructure) {
			return &engine.Result{Outcome: "lost", Error: msg}
		}
		return &engine.Result{Outcome: "failed", Error: msg}
	}
	return nil
}

// downloadFile fetches one file into local, carrying on after a broken
// connection from what it has, and checks its SHA-256.
func (r *Runner) downloadFile(ctx context.Context, job *engine.Job, artifact string, fl engine.ArtifactFileInfo, local string) error {
	for whole := 0; whole < 2; whole++ {
		dst, err := os.OpenFile(local, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		var have int64
		err = retry(ctx, func() error {
			body, err := r.Client.Download(ctx, job.LeaseID, artifact, fl.Path, have)
			if err != nil {
				return err
			}
			defer body.Close()
			n, err := io.Copy(dst, io.LimitReader(body, fl.Size-have+1))
			have += n
			if err == nil && have < fl.Size {
				err = io.ErrUnexpectedEOF
			}
			return err
		})
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if have != fl.Size {
			return fmt.Errorf("the server sent %d bytes, not %d", have, fl.Size)
		}
		sum, err := fileSHA256(local)
		if err != nil {
			return err
		}
		if sum == fl.SHA256 {
			return nil
		}
	}
	return errors.New("the file received does not have the SHA-256 it was kept with, twice")
}

func fileSHA256(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// collected is what a runner collects after a step: report files to send
// with its result. Artifacts are uploaded on their own.
type collected struct {
	tests, findings *engine.CollectedReports
}

// outcomeOf is how an attempt ended, for the artifacts kept after it:
// succeeded, failed (a timeout too), or canceled when someone asked.
func outcomeOf(res engine.Result, requested bool) string {
	if requested {
		return "canceled"
	}
	return res.Outcome
}

// findingCollector returns what collects a step's SARIF files, or nil.
func (r *Runner) findingCollector(job *engine.Job, spec executor.Spec, sys func(string, ...any)) func() *engine.CollectedReports {
	if job.Reports == nil || len(job.Reports.Findings) == 0 {
		return nil
	}
	return func() *engine.CollectedReports {
		lim := r.Reports.WithDefaults()
		if job.Reports.FindingsMaxBytes > 0 {
			lim.MaxBytes = min(lim.MaxBytes, job.Reports.FindingsMaxBytes)
			lim.MaxFileBytes = min(lim.MaxFileBytes, lim.MaxBytes)
		}
		ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
		defer cancel()
		got, err := executor.Collect(ctx, r.execFor(job), spec, job.Reports.Findings, lim)
		out := &engine.CollectedReports{Files: []engine.ReportFile{}}
		if got != nil {
			for _, f := range got.Files {
				out.Files = append(out.Files, engine.ReportFile{Path: f.Path, Data: f.Data, Size: f.Size, Cut: f.Cut})
			}
			out.Notes = got.Notes
		}
		if err != nil {
			msg := "Could not collect the SARIF files: " + spec.Mask(err.Error())
			if errors.Is(err, context.DeadlineExceeded) {
				msg = fmt.Sprintf("Collecting the SARIF files took longer than %s; stopped", collectTimeout)
			}
			out.Notes = append(out.Notes, msg)
		}
		var size int64
		for _, f := range out.Files {
			size += int64(len(f.Data))
		}
		switch n := len(out.Files); {
		case n == 1:
			sys("Findings: collected %s (%s)", out.Files[0].Path, sizeText(size))
		case n > 1:
			sys("Findings: collected %d files (%s)", n, sizeText(size))
		}
		for _, note := range out.Notes {
			sys("Findings: %s", note)
		}
		return out
	}
}
