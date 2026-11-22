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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Build caches on the runner (schema 25). Before a step's script, each of
// its caches is looked up on the server and, if there is an entry, restored
// into the workspace; after the script, each whose save applies and that
// was not restored by an exact hit is packed and sent. Both go through the
// same machinery as artifacts (executor.Stage, executor.Place), so every
// executor works alike.
//
// A cache is an optimisation and never changes a step's outcome: whatever
// goes wrong here is said in the step's log and in the result the runner
// reports, and the step runs on.

// ---- client ----

// cacheRoute is a runner route under /api/runner/v1/caches.
func (c *Client) cacheCall(ctx context.Context, route string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, c.Server+"/api/runner/v1/caches/"+route, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return c.cacheReply(route, resp, data, out)
}

func (c *Client) cacheReply(route string, resp *http.Response, data []byte, out any) error {
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized {
		c.contact.Store(time.Now().UnixMilli())
	}
	switch {
	case resp.StatusCode == http.StatusGone:
		return ErrLeaseLost
	case resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &httpError{code: resp.StatusCode, msg: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("caches/%s: %w", route, err)
		}
	}
	return nil
}

// CacheLookup asks for the entry a step should restore.
func (c *Client) CacheLookup(ctx context.Context, lease string, in engine.CacheLookup) (*engine.CacheLookupResult, error) {
	var out engine.CacheLookupResult
	err := c.cacheCall(ctx, "lookup", struct {
		LeaseID string `json:"lease_id"`
		engine.CacheLookup
	}{lease, in}, &out)
	return &out, err
}

// CacheBegin declares an entry to save.
func (c *Client) CacheBegin(ctx context.Context, lease string, in engine.BeginCache) (*engine.BegunCache, error) {
	var out engine.BegunCache
	err := c.cacheCall(ctx, "begin", struct {
		LeaseID string `json:"lease_id"`
		engine.BeginCache
	}{lease, in}, &out)
	return &out, err
}

// CacheCommit keeps an entry whose bytes have all been sent.
func (c *Client) CacheCommit(ctx context.Context, lease, id string) (*engine.CommittedCache, error) {
	var out engine.CommittedCache
	err := c.cacheCall(ctx, "commit", map[string]string{"lease_id": lease, "cache_id": id}, &out)
	return &out, err
}

// CachePutChunk sends one chunk of an entry, returning how many bytes of it
// the server has.
func (c *Client) CachePutChunk(ctx context.Context, lease, id string, offset int64, data []byte) (int64, error) {
	sum := sha256.Sum256(data)
	q := url.Values{"lease_id": {lease}, "cache_id": {id}, "offset": {strconv.FormatInt(offset, 10)}, "sha256": {hex.EncodeToString(sum[:])}}
	resp, err := c.do(ctx, http.MethodPut, c.Server+"/api/runner/v1/caches/chunk?"+q.Encode(), "application/octet-stream", data)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var reply struct {
		Received int64  `json:"received"`
		Error    string `json:"error"`
	}
	_ = json.Unmarshal(body, &reply)
	if resp.StatusCode == http.StatusConflict && strings.Contains(reply.Error, "send the chunk that starts there") {
		c.contact.Store(time.Now().UnixMilli())
		return 0, &chunkOffset{Received: reply.Received}
	}
	if err := c.cacheReply("chunk", resp, body, nil); err != nil {
		return 0, err
	}
	return reply.Received, nil
}

// CacheDownload streams an entry from offset.
func (c *Client) CacheDownload(ctx context.Context, lease, id string, offset int64) (io.ReadCloser, error) {
	body, _ := json.Marshal(map[string]any{"lease_id": lease, "cache_id": id, "offset": offset})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/runner/v1/caches/download", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	tok, err := c.bearer(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	hc := *c.HTTP
	hc.Timeout = 0 // the whole body may take longer than the client's timeout
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		c.contact.Store(time.Now().UnixMilli())
		return resp.Body, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return nil, c.cacheReply("download", resp, data, nil)
}

// CacheDiscard asks the server to end an entry that cannot be restored
// because it is damaged.
func (c *Client) CacheDiscard(ctx context.Context, lease, id, why string) error {
	return c.cacheCall(ctx, "discard", map[string]string{"lease_id": lease, "cache_id": id, "reason": why}, nil)
}

// ---- one attempt's caches ----

// cacheTimeout bounds how long restoring, or saving, one step's caches may
// take.
const cacheTimeout = 30 * time.Minute

// What a key's files may be.
var cacheKeyLimits = executor.StageLimits{MaxFiles: 10000, MaxFileBytes: 256 << 20, MaxBytes: 1 << 30}

// cacheRun is the caches of one step attempt: what was found when they were
// restored, and what each did, for the result.
type cacheRun struct {
	r        *Runner
	job      *engine.Job
	spec     executor.Spec
	sys      func(string, ...any)
	platform string
	dir      string
	tasks    []*cacheTask

	// image is what the step's image resolved to, once imageDone.
	image     executor.ImageIdentity
	imageDone bool
}

type cacheTask struct {
	entry engine.JobCacheEntry
	// key is the key worked out for the step; empty if it could not be, in
	// which case skip says why and the cache is neither restored nor saved.
	key  string
	skip string
	// keyParts is what the key is made of, in words; keyIn is the input it
	// was worked out from (its files' contents as they were before the step).
	keyParts []executor.KeyPart
	keyIn    executor.CacheKeyInput
	// base is the entry the restore used, or found too old: what a save
	// starts from.
	base *engine.CacheEntry
	// restored is what the restore found: hit, fallback or miss.
	restored string
	res      engine.CacheResult
}

// newCacheRun prepares a step attempt's caches, or returns nil for a step
// that has none.
func (r *Runner) newCacheRun(job *engine.Job, spec executor.Spec, sys func(string, ...any)) *cacheRun {
	if job.Cache == nil || len(job.Cache.Caches) == 0 {
		return nil
	}
	cr := &cacheRun{r: r, job: job, spec: spec, sys: sys, platform: r.cachePlatform(), dir: filepath.Join(r.DataDir, "caches", job.LeaseID)}
	for _, e := range job.Cache.Caches {
		cr.tasks = append(cr.tasks, &cacheTask{entry: e, res: engine.CacheResult{Name: e.Name, Result: engine.CacheMiss}})
	}
	return cr
}

func (r *Runner) cachePlatform() string {
	if r.CachePlatform != "" {
		return r.CachePlatform
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

func (cr *cacheRun) results() []engine.CacheResult {
	if cr == nil {
		return nil
	}
	out := make([]engine.CacheResult, 0, len(cr.tasks))
	for _, t := range cr.tasks {
		out = append(out, t.res)
	}
	return out
}

func (cr *cacheRun) clean() { _ = os.RemoveAll(cr.dir) }

// mask hides secret values in what goes in the log.
func (cr *cacheRun) mask(s string) string {
	if cr.spec.Mask != nil {
		return cr.spec.Mask(s)
	}
	return s
}

func shortKey(k string) string {
	if len(k) > 4 {
		return k[:4] + "…"
	}
	return k
}

func cacheSize(n int64) string {
	if n >= 1<<30 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<30)), ".0") + " GiB"
	}
	return sizeText(n)
}

// ageText is a duration of seconds as days, hours or minutes.
func ageText(s int64) string {
	switch d := time.Duration(s) * time.Second; {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " days"
	case d >= 2*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " hours"
	case d >= 2*time.Minute:
		return strconv.Itoa(int(d.Minutes())) + " minutes"
	}
	return "under 2 minutes"
}

func seconds(d time.Duration) float64 { return d.Round(100 * time.Millisecond).Seconds() }

func secondsText(d time.Duration) string {
	return strconv.Itoa(int(d.Round(time.Second).Seconds())) + " s"
}

// restore looks each cache up and puts what it finds in the workspace.
// Nothing here fails the step.
func (cr *cacheRun) restore(parent context.Context, out executor.Sink) {
	defer cr.clean()
	defer cr.markBack()
	ctx, cancel := context.WithTimeout(parent, cacheTimeout)
	defer cancel()
	if p := cr.job.Cache.Problem; p != "" {
		for _, t := range cr.tasks {
			t.skip = p
			t.res.Reason = p
			cr.sys("Cache %s: not used: %s", t.entry.Name, cr.mask(p))
		}
		return
	}
	for _, t := range cr.tasks {
		if ctx.Err() != nil {
			t.skip = "the step was stopped before it could be restored"
			return
		}
		cr.restoreOne(ctx, t, out)
	}
}

func (cr *cacheRun) restoreOne(ctx context.Context, t *cacheTask, out executor.Sink) {
	name := t.entry.Name
	start := time.Now()
	t.key, t.keyParts, t.skip = cr.workOutKey(ctx, t)
	if t.skip != "" {
		t.res.Reason = t.skip
		cr.sys("Cache %s: not used: %s", name, cr.mask(t.skip))
		return
	}
	t.res.Key, t.res.KeyParts = t.key, keyParts(t.keyParts)
	var found *engine.CacheLookupResult
	err := retry(ctx, func() error {
		var err error
		found, err = cr.r.Client.CacheLookup(ctx, cr.job.LeaseID, engine.CacheLookup{Name: name, Key: t.key, Platform: cr.platform, Fallback: t.entry.Fallback})
		return err
	})
	if err != nil {
		reason := "the server could not be asked: " + err.Error()
		t.res.Reason = cr.mask(reason)
		cr.sys("Cache %s: no entry (%s); the step runs without it", name, t.res.Reason)
		return
	}
	t.res.MaxAgeSeconds = found.MaxAgeSeconds
	if found.Entry == nil && found.Expired != nil {
		ex := found.Expired
		t.restored, t.base = engine.CacheExpired, ex
		t.res.Result, t.res.EntryKey, t.res.Generation, t.res.AgeSeconds = engine.CacheExpired, ex.Key, ex.Generation, ex.AgeSeconds
		t.res.Reason = fmt.Sprintf("the entry is %s old, more than the %s this cache may be", ageText(ex.AgeSeconds), ageText(found.MaxAgeSeconds))
		cr.sys("Cache %s: the entry with this key (generation %d) is %s old, more than the %s this cache may be; it is not restored and the step rebuilds it",
			name, ex.Generation, ageText(ex.AgeSeconds), ageText(found.MaxAgeSeconds))
		return
	}
	if found.Entry == nil {
		t.restored = engine.CacheMiss
		t.res.Reason = found.Reason
		if found.Reason != "" {
			cr.sys("Cache %s: no entry (%s); the step runs without it", name, cr.mask(found.Reason))
		} else {
			cr.sys("Cache %s: no entry; the step runs without it", name)
		}
		return
	}
	en := found.Entry
	if err := cr.fetchEntry(ctx, t, en, out); err != nil {
		reason := cr.mask(err.Error())
		t.res.Reason = "not restored: " + reason
		cr.sys("Cache %s: not restored: %s; the step runs without it", name, reason)
		if errors.Is(err, errCorrupted) || errors.Is(err, executor.ErrCacheArchive) {
			// Entries are never changed, so this one would fail every
			// restore: have it ended, so that this step saves a good one.
			if derr := cr.r.Client.CacheDiscard(ctx, cr.job.LeaseID, en.ID, reason); derr == nil {
				cr.sys("Cache %s: the damaged entry was ended; this run saves a new one", name)
				t.restored = engine.CacheMiss
			}
		}
		return
	}
	took := time.Since(start)
	t.res.Bytes, t.res.Scope, t.res.Seconds = en.Bytes, en.Scope, seconds(took)
	// The entry restored is recorded as it is: its own key (which on a
	// fallback is not the step's), generation and age.
	t.base = en
	t.res.EntryKey, t.res.Generation, t.res.AgeSeconds = en.Key, en.Generation, en.AgeSeconds
	if en.Exact {
		t.restored, t.res.Result = engine.CacheHit, engine.CacheHit
		cr.sys("Cache %s: restored key %s (exact, generation %d, %s old, %s, %s)", name, shortKey(en.Key), en.Generation, ageText(en.AgeSeconds), cacheSize(en.Bytes), secondsText(took))
		return
	}
	t.restored, t.res.Result = engine.CacheFallback, engine.CacheFallback
	cr.sys("Cache %s: restored key %s (fallback: the step's key is %s; generation %d, %s old, %s, %s)", name, shortKey(en.Key), shortKey(t.key), en.Generation, ageText(en.AgeSeconds), cacheSize(en.Bytes), secondsText(took))
}

// workOutKey stages the key's files and hashes them with everything else the
// key is made of. It returns the key and what it is made of, or why there is
// none.
func (cr *cacheRun) workOutKey(ctx context.Context, t *cacheTask) (string, []executor.KeyPart, string) {
	in := executor.CacheKeyInput{Platform: cr.platform, Name: t.entry.Name, Text: t.entry.KeyText, Image: cr.imageIdentity(ctx)}
	if len(t.entry.KeyFiles) > 0 {
		dir := filepath.Join(cr.dir, "key-"+t.entry.Name)
		staged, err := executor.StageWorkspace(ctx, cr.r.execFor(cr.job), cr.spec, t.entry.KeyFiles, cacheKeyLimits, dir)
		defer os.RemoveAll(dir)
		if err != nil {
			if errors.Is(err, executor.ErrNotKept) {
				if staged != nil && len(staged.Files) == 0 {
					return "", nil, "key.files matched no file, so there is nothing to tell this cache's key from another's"
				}
				return "", nil, "the key's files cannot be read: " + strings.TrimPrefix(err.Error(), executor.ErrNotKept.Error()+": ")
			}
			return "", nil, "the key's files cannot be read: " + err.Error()
		}
		in.Files = staged.Files
	}
	vs, why := cr.versionValues(ctx, t)
	if why != "" {
		return "", nil, why
	}
	in.Versions = vs
	t.keyIn = in
	key, parts := executor.CacheKey(in)
	return key, parts, ""
}

func keyParts(in []executor.KeyPart) []engine.CacheKeyPart {
	out := make([]engine.CacheKeyPart, len(in))
	for i, p := range in {
		out[i] = engine.CacheKeyPart{Label: p.Label, Value: p.Value}
	}
	return out
}

// fetchEntry downloads an entry, checks it, unpacks it safely and puts it in
// the workspace.
func (cr *cacheRun) fetchEntry(ctx context.Context, t *cacheTask, en *engine.CacheEntry, out executor.Sink) error {
	dir := filepath.Join(cr.dir, "restore-"+t.entry.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	archive := filepath.Join(dir, "archive")
	if err := cr.download(ctx, en, archive); err != nil {
		return err
	}
	lim := executor.UnpackLimits{MaxFiles: engine.MaxCacheFiles}
	if m := cr.job.Cache.MaxEntryBytes; m > 0 {
		lim.MaxTotalBytes = max(m*16, 64<<20)
	}
	un, err := executor.UnpackCache(ctx, archive, filepath.Join(dir, "files"), t.entry.Paths, lim)
	if err != nil {
		return err
	}
	if err := executor.Place(ctx, cr.r.execFor(cr.job), cr.spec, un.Files, out); err != nil {
		if errors.Is(err, executor.ErrInfrastructure) {
			return fmt.Errorf("putting the files in the workspace: %v", err)
		}
		return fmt.Errorf("putting the files in the workspace: %w", err)
	}
	if un.Skipped > 0 {
		cr.sys("Cache %s: %d files in the entry are not under the cache's paths now and were left out", t.entry.Name, un.Skipped)
	}
	return nil
}

// download fetches an entry into a file, carrying on after a broken
// connection from what it has, and checks its SHA-256. A copy that does not
// match is fetched again once.
func (cr *cacheRun) download(ctx context.Context, en *engine.CacheEntry, local string) error {
	for whole := 0; whole < 2; whole++ {
		dst, err := os.OpenFile(local, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		var have int64
		err = retry(ctx, func() error {
			body, err := cr.r.Client.CacheDownload(ctx, cr.job.LeaseID, en.ID, have)
			if err != nil {
				return err
			}
			defer body.Close()
			n, err := io.Copy(dst, io.LimitReader(body, en.Bytes-have+1))
			have += n
			if err == nil && have < en.Bytes {
				err = io.ErrUnexpectedEOF
			}
			return err
		})
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("downloading: %w", err)
		}
		if have != en.Bytes {
			return fmt.Errorf("the server sent %d bytes, not %d", have, en.Bytes)
		}
		sum, err := fileSHA256(local)
		if err != nil {
			return err
		}
		if sum == en.SHA256 {
			return nil
		}
	}
	return fmt.Errorf("the entry received does not have the SHA-256 it was saved with, twice: it is %w", errCorrupted)
}

// errCorrupted says an entry's bytes are not what was saved.
var errCorrupted = errors.New("corrupted")

// ---- saving ----

// save packs and sends each cache whose save applies to how the attempt
// ended and that an exact hit did not already have.
func (cr *cacheRun) save(outcome string) {
	defer cr.clean()
	defer cr.dropScratch()
	ctx, cancel := context.WithTimeout(context.Background(), cacheTimeout)
	defer cancel()
	for _, t := range cr.tasks {
		if t.skip != "" || !(&pipeline.Cache{Save: t.entry.Save}).SaveAfter(outcome) {
			continue
		}
		cr.saveOne(ctx, t)
	}
}

// notSaved records and says why a cache was not saved.
func (cr *cacheRun) notSaved(t *cacheTask, why string) {
	why = cr.mask(why)
	if t.restored == "" && t.res.Reason != "" {
		why = t.res.Reason + "; " + why // the restore failed, and that is worth keeping
	}
	t.res.Restored, t.res.Result, t.res.Reason = t.restored, engine.CacheNotSaved, why
	t.res.Bytes, t.res.Scope = 0, ""
	cr.sys("Cache %s: not saved: %s", t.entry.Name, why)
}

func (cr *cacheRun) saveOne(ctx context.Context, t *cacheTask) {
	name := t.entry.Name
	start := time.Now()
	changed, why := cr.rekey(ctx, t)
	if why != "" {
		cr.notSaved(t, why)
		return
	}
	dir := filepath.Join(cr.dir, "save-"+name)
	defer os.RemoveAll(dir)
	maxEntry := cr.job.Cache.MaxEntryBytes
	lim := executor.StageLimits{MaxFiles: engine.MaxCacheFiles, MaxFileBytes: 8 << 30, MaxBytes: 32 << 30, NoGit: true}
	if maxEntry > 0 {
		lim.MaxBytes = maxInt64(maxEntry*16, 64<<20)
	}
	staged, err := executor.StageWorkspace(ctx, cr.r.execFor(cr.job), cr.spec, t.entry.Paths, lim, filepath.Join(dir, "stage"))
	if staged != nil && len(staged.Notes) > 0 {
		for i, n := range staged.Notes {
			if i == 3 {
				cr.sys("Cache %s: and %d more notes", name, len(staged.Notes)-3)
				break
			}
			cr.sys("Cache %s: %s", name, cr.mask(n))
		}
	}
	if err != nil {
		cr.notSaved(t, strings.TrimPrefix(err.Error(), executor.ErrNotKept.Error()+": "))
		return
	}
	if why := cr.refuseCredentials(staged.Files); why != "" {
		cr.notSaved(t, why)
		return
	}
	// A step that started from an entry with this key and left the paths as
	// they were restored has nothing to add; one that changed them (an
	// install that fetched something newer) saves the next generation of the
	// key, and the entry it replaces ends.
	content := executor.ContentDigest(staged.Files)
	if t.restored == engine.CacheHit && !changed && t.base != nil && t.base.Content == content {
		cr.sys("Cache %s: the step left the cached paths as they were restored; nothing to save", name)
		return
	}
	base := ""
	if !changed && t.base != nil {
		base = t.base.ID
	}
	archive, err := executor.PackCache(staged.Files, filepath.Join(dir, "archive"))
	if err != nil {
		cr.notSaved(t, "packing the files failed: "+err.Error())
		return
	}
	os.RemoveAll(filepath.Join(dir, "stage"))
	if maxEntry > 0 && archive.Bytes > maxEntry {
		cr.notSaved(t, fmt.Sprintf("this cache is %s; an entry may hold at most %s (cache policy max_entry_gib)", cacheSize(archive.Bytes), cacheSize(maxEntry)))
		return
	}
	var begun *engine.BegunCache
	err = retry(ctx, func() error {
		var err error
		begun, err = cr.r.Client.CacheBegin(ctx, cr.job.LeaseID, engine.BeginCache{Name: name, Key: t.key, Platform: cr.platform,
			Bytes: archive.Bytes, Files: archive.Files, SHA256: archive.SHA256, Base: base, Content: content})
		return err
	})
	if err != nil {
		cr.notSaved(t, refusalText(err))
		return
	}
	if begun.Exists {
		cr.notSaved(t, begun.Reason)
		return
	}
	if begun.Evicted > 0 {
		cr.sys("Cache %s: %d older entries of this project were ended to make room (cache policy max_project_gib)", name, begun.Evicted)
	}
	if err := cr.send(ctx, begun, archive); err != nil {
		cr.notSaved(t, refusalText(err))
		return
	}
	var done *engine.CommittedCache
	err = retry(ctx, func() error {
		var err error
		done, err = cr.r.Client.CacheCommit(ctx, cr.job.LeaseID, begun.ID)
		return err
	})
	if err != nil {
		cr.notSaved(t, refusalText(err))
		return
	}
	if done.Exists {
		cr.notSaved(t, "an entry with this key was saved already, and entries are never changed")
		return
	}
	t.res.Restored, t.res.Result, t.res.Reason = t.restored, engine.CacheSaved, ""
	t.res.Bytes, t.res.Scope, t.res.Seconds = archive.Bytes, begun.Scope, seconds(time.Since(start))
	t.res.Key, t.res.SavedGeneration = t.key, begun.Generation
	if begun.Generation > 1 {
		cr.sys("Cache %s: saved %s as generation %d of key %s (the step changed what was restored)", name, cacheSize(archive.Bytes), begun.Generation, shortKey(t.key))
		return
	}
	cr.sys("Cache %s: saved %s as key %s", name, cacheSize(archive.Bytes), shortKey(t.key))
}

// refuseCredentials is the message for a cache that holds files that look
// like credentials, or "". Such a cache is never saved: every run that may
// restore it would receive them.
func (cr *cacheRun) refuseCredentials(files []executor.StagedFile) string {
	found := executor.CredentialFiles(files)
	if len(found) == 0 {
		return ""
	}
	var shown []string
	for i, f := range found {
		if i == 5 {
			shown = append(shown, fmt.Sprintf("and %d more", len(found)-5))
			break
		}
		shown = append(shown, f.Path+" ("+f.Why+")")
	}
	return fmt.Sprintf("it holds %d file(s) that look like credentials: %s. Every run that may restore a cache receives what is in it, so it is not saved. Narrow the cache's paths so they leave the file out (cache .m2/repository, not the whole .m2), and use short-lived registry credentials that are not written into the workspace",
		len(found), strings.Join(shown, ", "))
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// refusalText turns an error from the server into plain words.
func refusalText(err error) string {
	var he *httpError
	switch {
	case errors.As(err, &he):
		return he.msg
	case errors.Is(err, ErrLeaseLost):
		return "the server no longer holds this step"
	}
	return err.Error()
}

// send uploads an archive in chunks from where the server has it.
func (cr *cacheRun) send(ctx context.Context, begun *engine.BegunCache, a *executor.CacheArchive) error {
	chunk := begun.ChunkBytes
	if chunk <= 0 {
		chunk = engine.ArtifactChunkBytes
	}
	src, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer src.Close()
	buf := make([]byte, chunk)
	offset := begun.Received
	restarts := 0
	for offset < a.Bytes {
		offset -= offset % chunk
		n, err := src.ReadAt(buf[:min(chunk, a.Bytes-offset)], offset)
		if err != nil && (!errors.Is(err, io.EOF) || int64(n) != min(chunk, a.Bytes-offset)) {
			return err
		}
		var got int64
		err = retry(ctx, func() error {
			var err error
			got, err = cr.r.Client.CachePutChunk(ctx, cr.job.LeaseID, begun.ID, offset, buf[:n])
			var co *chunkOffset
			if errors.As(err, &co) {
				got, err = co.Received, nil
			}
			return err
		})
		if err != nil {
			return err
		}
		if got <= offset && got < a.Bytes {
			restarts++
			if restarts > 5 {
				return errors.New("sending the entry made no progress")
			}
		}
		offset = got
	}
	return nil
}

// markBack tells an executor that carries the workspace through the Station which caches are saved after the
// step, so that it hands back what the step leaves in those paths (and only those).
func (cr *cacheRun) markBack() {
	if cr.spec.Cache == nil {
		return
	}
	cr.spec.Cache.Back = nil
	for _, t := range cr.tasks {
		if t.skip == "" && t.entry.Save != pipeline.CacheSaveNever {
			cr.spec.Cache.Back = append(cr.spec.Cache.Back, t.entry.Paths...)
		}
	}
}

// dropScratch lets an executor drop the step's caches once they are saved.
func (cr *cacheRun) dropScratch() {
	if sc, ok := cr.r.Exec.(executor.StepScratcher); ok {
		sc.DropScratch(cr.spec)
	}
}
