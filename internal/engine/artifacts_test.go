package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const artifactsPipeline = `
pipeline: demo
steps:
  build:
    image: x
    run: make
    artifacts:
      - {name: binary, paths: [bin/app], retain: 2d}
  deploy:
    image: x
    run: ship
    needs: build
    fetch:
      - {from: build, name: binary, into: out}
`

// openStore opens an engine whose artifacts are kept in a directory.
func openStore(t *testing.T, addr string, clk *clock, dir string, o ArtifactOptions) *Engine {
	t.Helper()
	store, err := blobstore.NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	o.Store = store
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Artifacts: o})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newStoreEngine(t *testing.T, o ArtifactOptions) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	return openStore(t, testdb.New(t), clk, t.TempDir(), o), clk
}

type upFile struct {
	path string
	data []byte
	mode uint32
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func infos(files []upFile) []ArtifactFileInfo {
	var out []ArtifactFileInfo
	for _, f := range files {
		out = append(out, ArtifactFileInfo{Path: f.path, Size: int64(len(f.data)), SHA256: sum(f.data), Mode: f.mode})
	}
	return out
}

// sendAll sends what the server lacks of each file, chunk by chunk.
func sendAll(t *testing.T, e *Engine, lease string, b *BegunArtifact, files []upFile) {
	t.Helper()
	for i, bf := range b.Files {
		var f upFile
		for _, x := range files {
			if x.path == bf.Path {
				f = x
			}
		}
		if bf.Done {
			continue
		}
		for off := bf.Received; off < int64(len(f.data)); {
			end := min(off+ArtifactChunkBytes, int64(len(f.data)))
			got, err := e.PutArtifactChunk(context.Background(), lease, b.ID, f.path, off, sum(f.data[off:end]), f.data[off:end])
			if err != nil {
				t.Fatalf("file %d chunk at %d: %v", i, off, err)
			}
			off = got
		}
	}
}

func keep(t *testing.T, e *Engine, lease, name string, files []upFile) *ArtifactSummary {
	t.Helper()
	b, err := e.BeginArtifact(lease, BeginArtifact{Name: name, Files: infos(files)})
	if err != nil {
		t.Fatal(err)
	}
	sendAll(t, e, lease, b, files)
	s, err := e.CommitArtifact(context.Background(), lease, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readFetch(t *testing.T, e *Engine, lease, artifact, path string) []byte {
	t.Helper()
	f, err := e.OpenFetch(context.Background(), lease, artifact, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Body.Close()
	b, err := io.ReadAll(f.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func big(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed
	}
	return b
}

// An artifact goes up in chunks, is recorded in the event that ends its
// step, and a later step fetches it, file by file, whole.
func TestArtifactRoundTrip(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	if j.Artifacts == nil || len(j.Artifacts.Keep) != 1 || j.Artifacts.Keep[0].Name != "binary" || j.Artifacts.Problem != "" ||
		j.Artifacts.ChunkBytes != ArtifactChunkBytes {
		t.Fatalf("job artifacts %+v", j.Artifacts)
	}
	files := []upFile{
		{"bin/app", big(ArtifactChunkBytes+12345, 1), 0o755},
		{"bin/empty", nil, 0o644},
		{"bin/notes.txt", []byte("hello"), 0o600},
	}
	s := keep(t, e, j.LeaseID, "binary", files)
	if s.Files != 3 || s.Bytes != int64(len(files[0].data)+5) {
		t.Fatalf("summary %+v", s)
	}
	ok(t, e, j, nil)
	evs, _ := e.Events(id)
	var recorded []ArtifactSummary
	for _, ev := range evs {
		if ev.Type == StepFinished && ev.Data.Step == "build" {
			recorded = ev.Data.Artifacts
		}
	}
	if len(recorded) != 1 || recorded[0].Name != "binary" || recorded[0].Files != 3 {
		t.Fatalf("the event records %+v", recorded)
	}
	d := take(t, e)
	if d.StepID != "deploy" || len(d.Fetch) != 1 {
		t.Fatalf("job %s fetch %+v", d.StepID, d.Fetch)
	}
	f := d.Fetch[0]
	if f.Problem != "" || f.Into != "out" || len(f.Files) != 3 || f.Files[0].Path != "bin/app" || f.Files[0].Mode != 0o755 ||
		f.Files[2].Mode != 0o644 || f.Files[0].SHA256 != sum(files[0].data) {
		t.Fatalf("fetch %+v", f)
	}
	if got := readFetch(t, e, d.LeaseID, f.ArtifactID, "bin/app"); !bytes.Equal(got, files[0].data) {
		t.Fatalf("fetched %d bytes, not the file", len(got))
	}
	if got := readFetch(t, e, d.LeaseID, f.ArtifactID, "bin/empty"); len(got) != 0 {
		t.Fatalf("empty file: %q", got)
	}
	// From an offset, for a download that broke.
	g, err := e.OpenFetch(context.Background(), d.LeaseID, f.ArtifactID, "bin/app", ArtifactChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(g.Body)
	g.Body.Close()
	if !bytes.Equal(rest, files[0].data[ArtifactChunkBytes:]) {
		t.Fatal("reading from an offset")
	}
	// Only what the step fetches: not by the step that kept it.
	if _, err := e.OpenFetch(context.Background(), d.LeaseID, f.ArtifactID, "nope", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a file the artifact does not have: %v", err)
	}
	ok(t, e, d, nil)
	if _, err := e.OpenFetch(context.Background(), d.LeaseID, f.ArtifactID, "bin/app", 0); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("after the lease ended: %v", err)
	}
	// The UI lists it and serves its files.
	list, err := e.RunArtifacts(id)
	if err != nil || len(list.Artifacts) != 1 || list.Artifacts[0].Status != "kept" || len(list.Artifacts[0].Files) != 3 ||
		list.Artifacts[0].ExpiresAt != list.Artifacts[0].KeptAt+(48*time.Hour).Milliseconds() {
		t.Fatalf("listed %+v, %v", list, err)
	}
	if sum, _ := e.Summary(id); sum.Artifacts != 1 {
		t.Fatalf("run summary counts %d artifacts", sum.Artifacts)
	}
}

// A file whose content is kept already is not sent again.
func TestArtifactContentIsSharedBySHA256(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	data := big(1000, 3)
	for i := 0; i < 2; i++ {
		create(t, e, artifactsPipeline, Trigger{Event: "manual"})
		j := take(t, e)
		b, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", data, 0o755}})})
		if err != nil {
			t.Fatal(err)
		}
		if want := i == 1; b.Files[0].Done != want {
			t.Fatalf("run %d: done %v", i, b.Files[0].Done)
		}
		sendAll(t, e, j.LeaseID, b, []upFile{{"bin/app", data, 0o755}})
		if _, err := e.CommitArtifact(context.Background(), j.LeaseID, b.ID); err != nil {
			t.Fatal(err)
		}
		ok(t, e, j, nil)
		ok(t, e, take(t, e), nil)
	}
	var blobs int
	e.db.QueryRow(`SELECT COUNT(*) FROM artifact_blobs`).Scan(&blobs)
	if blobs != 1 {
		t.Fatalf("%d blobs for one content", blobs)
	}
}

// An upload that stops part-way carries on where it was; chunks sent twice
// are harmless; one that does not start where the upload is says where.
func TestInterruptedUploadCarriesOn(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	data := big(2*ArtifactChunkBytes+10, 9)
	files := []upFile{{"bin/app", data, 0o755}}
	b, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos(files)})
	if err != nil {
		t.Fatal(err)
	}
	first := data[:ArtifactChunkBytes]
	if got, err := e.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "bin/app", 0, sum(first), first); err != nil || got != ArtifactChunkBytes {
		t.Fatalf("first chunk: %d %v", got, err)
	}
	// Again: accepted, nothing changes.
	if got, err := e.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "bin/app", 0, sum(first), first); err != nil || got != ArtifactChunkBytes {
		t.Fatalf("first chunk again: %d %v", got, err)
	}
	// Skipping a chunk: told where to carry on.
	last := data[2*ArtifactChunkBytes:]
	_, err = e.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "bin/app", 2*ArtifactChunkBytes, sum(last), last)
	var co *ChunkOffsetError
	if !errors.As(err, &co) || co.Received != ArtifactChunkBytes {
		t.Fatalf("a chunk out of order: %v", err)
	}
	// A damaged chunk.
	if _, err := e.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "bin/app", ArtifactChunkBytes, sum([]byte("other")), data[ArtifactChunkBytes:2*ArtifactChunkBytes]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a chunk that does not match its SHA-256: %v", err)
	}
	// Committing before every file is sent.
	if _, err := e.CommitArtifact(context.Background(), j.LeaseID, b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("an early commit: %v", err)
	}
	// The runner asks again (it restarted the upload): the server says how
	// far it got, and the rest goes up.
	again, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos(files)})
	if err != nil || again.ID != b.ID || again.Files[0].Received != ArtifactChunkBytes || again.Files[0].Done {
		t.Fatalf("asked again: %+v %v", again, err)
	}
	// Declaring other files under the same name is a mistake.
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/x", data, 0}})}); !errors.Is(err, ErrConflict) {
		t.Fatalf("other files: %v", err)
	}
	sendAll(t, e, j.LeaseID, again, files)
	if _, err := e.CommitArtifact(context.Background(), j.LeaseID, b.ID); err != nil {
		t.Fatal(err)
	}
}

// Content that does not have the SHA-256 it was declared with is refused,
// and the file starts again.
func TestChecksumMismatch(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	real := []byte("the real content")
	b, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary",
		Files: []ArtifactFileInfo{{Path: "bin/app", Size: int64(len(real)), SHA256: sum([]byte("something else!!"))}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "bin/app", 0, sum(real), real)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	again, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary",
		Files: []ArtifactFileInfo{{Path: "bin/app", Size: int64(len(real)), SHA256: sum([]byte("something else!!"))}}})
	if err != nil || again.Files[0].Received != 0 || again.Files[0].Done {
		t.Fatalf("after a mismatch: %+v %v", again, err)
	}
	if _, err := e.CommitArtifact(context.Background(), j.LeaseID, b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("committing a file never stored: %v", err)
	}
}

// Limits: on one file, on a run, and on a project; and what may be
// declared at all.
func TestArtifactLimits(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{MaxFileBytes: 100, MaxRunBytes: 150, MaxProjectBytes: 250})
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	begin := func(name string, files ...upFile) error {
		_, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: name, Files: infos(files)})
		return err
	}
	for what, err := range map[string]error{
		"a file too large":        begin("binary", upFile{"a", big(101, 0), 0}),
		"a run too large":         begin("binary", upFile{"a", big(100, 0), 0}, upFile{"b", big(60, 0), 0}),
		"no files":                begin("binary"),
		"an undeclared artifact":  begin("other", upFile{"a", big(1, 0), 0}),
		"a path outside":          begin("binary", upFile{"../a", big(1, 0), 0}),
		"an absolute path":        begin("binary", upFile{"/etc/passwd", big(1, 0), 0}),
		"a path not clean":        begin("binary", upFile{"a/./b", big(1, 0), 0}),
		"a file listed twice":     begin("binary", upFile{"a", big(1, 0), 0}, upFile{"a", big(1, 0), 0}),
		"a control character":     begin("binary", upFile{"a\nb", big(1, 0), 0}),
		"an empty path":           begin("binary", upFile{"", big(1, 0), 0}),
		"a lease that is unknown": func() error { _, err := e.BeginArtifact("lease_nope", BeginArtifact{Name: "binary"}); return err }(),
	} {
		if err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	if err := begin("binary", upFile{"a", big(101, 0), 0}); !errors.Is(err, ErrQuota) {
		t.Fatalf("a file too large: %v", err)
	}
	keep(t, e, j.LeaseID, "binary", []upFile{{"a", big(100, 1), 0}, {"b", big(50, 2), 0}})
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	// A second run fits the project's quota once; a third does not.
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j2 := take(t, e)
	if err := func() error {
		_, err := e.BeginArtifact(j2.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"a", big(100, 3), 0}, {"b", big(1, 3), 0}})})
		return err
	}(); !errors.Is(err, ErrQuota) || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("over the project's quota: %v", err)
	}
	keep(t, e, j2.LeaseID, "binary", []upFile{{"a", big(100, 3), 0}})
	var used int64
	e.db.QueryRow(`SELECT bytes FROM artifact_usage WHERE project = 'demo'`).Scan(&used)
	if used != 250 {
		t.Fatalf("the project keeps %d bytes", used)
	}
}

// A step whose runner failed keeps nothing: its artifacts end, and their
// bytes go back to the quota.
func TestLostAttemptKeepsNothing(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", big(10, 1), 0}})
	// A second artifact left half-way.
	if err := e.Finish(j.LeaseID, Result{Outcome: "lost", Error: "the node went away"}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.RunArtifacts(id)
	if len(list.Artifacts) != 1 || list.Artifacts[0].Status != "failed" || !strings.Contains(list.Artifacts[0].Note, "runner") {
		t.Fatalf("after the runner failed: %+v", list.Artifacts)
	}
	var used int64
	e.db.QueryRow(`SELECT bytes FROM artifact_usage WHERE project = 'demo'`).Scan(&used)
	if used != 0 {
		t.Fatalf("the project still counts %d bytes", used)
	}
	// The next attempt keeps its own, and the step after fetches that one.
	j2 := take(t, e)
	if j2.Attempt != 2 {
		t.Fatalf("attempt %d", j2.Attempt)
	}
	keep(t, e, j2.LeaseID, "binary", []upFile{{"bin/app", big(10, 2), 0}})
	ok(t, e, j2, nil)
	d := take(t, e)
	if got := readFetch(t, e, d.LeaseID, d.Fetch[0].ArtifactID, "bin/app"); !bytes.Equal(got, big(10, 2)) {
		t.Fatal("fetched the lost attempt's file")
	}
}

// A step that ends while an upload is half-way leaves it failed.
func TestUnfinishedUploadFailsWithItsStep(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", big(10, 1), 0}})}); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j, nil)
	list, _ := e.RunArtifacts(id)
	if len(list.Artifacts) != 1 || list.Artifacts[0].Status != "failed" {
		t.Fatalf("%+v", list.Artifacts)
	}
	// The step that fetches it fails, saying why.
	d := take(t, e)
	if len(d.Fetch) != 1 || !strings.Contains(d.Fetch[0].Problem, "not kept") {
		t.Fatalf("fetch %+v", d.Fetch)
	}
}

// A step that keeps nothing that its successor fetches: the job says so.
func TestFetchOfAnArtifactNeverKept(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil)
	d := take(t, e)
	if len(d.Fetch) != 1 || d.Fetch[0].ArtifactID != "" || !strings.Contains(d.Fetch[0].Problem, "kept no artifact binary") {
		t.Fatalf("fetch %+v", d.Fetch)
	}
}

// A runner that does not say it knows artifacts is never given a step
// that keeps or fetches some: it would run it without them.
func TestOlderRunnersDoNotGetArtifactSteps(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	old := RunnerInfo{ID: "old", Name: "old"}
	if j, err := e.Lease(context.Background(), old, 0); err != nil || j != nil {
		t.Fatalf("an older runner got %+v, %v", j, err)
	}
	create(t, e, linear, Trigger{Event: "manual"})
	if j, err := e.Lease(context.Background(), old, 0); err != nil || j == nil {
		t.Fatalf("an older runner got no plain step: %v", err)
	}
}

// Without a store, steps that keep artifacts are told why none is kept.
func TestNoStore(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	if j.Artifacts == nil || !strings.Contains(j.Artifacts.Problem, "no artifact store") {
		t.Fatalf("job artifacts %+v", j.Artifacts)
	}
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("begin without a store: %v", err)
	}
}

// Deleting an artifact: only a kept one, of the run named, once.
func TestDeleteArtifact(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	s := keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", big(10, 1), 0}})
	ok(t, e, j, nil)
	other := create(t, e, linear, Trigger{Event: "manual"})
	if _, err := e.DeleteArtifact(other, s.ID, "ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("through another run: %v", err)
	}
	v, err := e.DeleteArtifact(id, s.ID, "ann")
	if err != nil || v.Status != "deleted" || v.EndedBy != "ann" {
		t.Fatalf("deleted %+v %v", v, err)
	}
	if _, err := e.DeleteArtifact(id, s.ID, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted twice: %v", err)
	}
	if _, err := e.OpenArtifactFile(context.Background(), id, s.ID, "bin/app", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a deleted artifact: %v", err)
	}
	d := take(t, e)
	if !strings.Contains(d.Fetch[0].Problem, "ann deleted it") {
		t.Fatalf("fetching a deleted artifact: %+v", d.Fetch)
	}
}

// Retention and removing contents, with two servers pruning at once: each
// artifact expires once, its bytes return to the quota once, and content
// another kept artifact still uses stays.
func TestPruningArtifactsWithTwoServers(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr, dir := testdb.New(t), t.TempDir()
	a := openStore(t, addr, clk, dir, ArtifactOptions{})
	b := openStore(t, addr, clk, dir, ArtifactOptions{})
	shared := big(1000, 5)
	run := func(e *Engine, src string, files []upFile) string {
		id := create(t, e, src, Trigger{Event: "manual"})
		j := take(t, e)
		keep(t, e, j.LeaseID, "binary", files)
		ok(t, e, j, nil)
		ok(t, e, take(t, e), nil)
		return id
	}
	short := run(a, artifactsPipeline, []upFile{{"bin/app", shared, 0}, {"bin/only-here", big(300, 6), 0}})
	long := run(b, strings.Replace(artifactsPipeline, "retain: 2d", "retain: 30d", 1), []upFile{{"bin/app", shared, 0}})
	clk.Add(3 * 24 * time.Hour)
	var wg sync.WaitGroup
	for range 3 {
		for _, e := range []*Engine{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); e.pruneArtifacts() }()
		}
	}
	wg.Wait()
	status := func(run string) string {
		l, _ := a.RunArtifacts(run)
		return l.Artifacts[0].Status
	}
	if status(short) != "expired" || status(long) != "kept" {
		t.Fatalf("statuses %s %s", status(short), status(long))
	}
	var used int64
	a.db.QueryRow(`SELECT bytes FROM artifact_usage WHERE project = 'demo'`).Scan(&used)
	if used != 1000 {
		t.Fatalf("the project counts %d bytes, want the 1000 still kept", used)
	}
	// Content is removed only after the grace period, and only what
	// nothing kept refers to.
	clk.Add(2 * time.Hour)
	for range 2 {
		for _, e := range []*Engine{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); e.pruneArtifacts() }()
		}
	}
	wg.Wait()
	var blobs int
	a.db.QueryRow(`SELECT COUNT(*) FROM artifact_blobs`).Scan(&blobs)
	if blobs != 1 {
		t.Fatalf("%d blobs left, want the shared one", blobs)
	}
	l, _ := a.RunArtifacts(long)
	f, err := a.OpenArtifactFile(context.Background(), long, l.Artifacts[0].ID, "bin/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f.Body)
	f.Body.Close()
	if !bytes.Equal(got, shared) {
		t.Fatal("the shared content was damaged")
	}
}

// A runner declaring a file with content the pruner is about to remove:
// either the declaration refreshes the blob first, and it stays, or the
// pruner claims it first, and the file is sent again. Never a kept file
// without content.
func TestDeclaringContentWhilePruning(t *testing.T) {
	e, clk := newStoreEngine(t, ArtifactOptions{})
	data := big(500, 8)
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	s := keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", data, 0}})
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	if _, err := e.DeleteArtifact(id, s.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	clk.Add(2 * time.Hour) // the blob is now unreferenced and old
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j2 := take(t, e)
	b, err := e.BeginArtifact(j2.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", data, 0}})})
	if err != nil || !b.Files[0].Done {
		t.Fatalf("declared %+v %v", b, err)
	}
	e.pruneArtifacts() // the declaration refreshed it: it stays
	if _, err := e.CommitArtifact(context.Background(), j2.LeaseID, b.ID); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j2, nil)
	d := take(t, e)
	if got := readFetch(t, e, d.LeaseID, d.Fetch[0].ArtifactID, "bin/app"); !bytes.Equal(got, data) {
		t.Fatal("the content is gone")
	}
}

// The pruner claims content between a declaration and its commit: the
// commit says which files to send again, and once sent all is well.
func TestCommitAfterContentWasRemoved(t *testing.T) {
	e, clk := newStoreEngine(t, ArtifactOptions{})
	data := big(500, 8)
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	s := keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", data, 0}})
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j2 := take(t, e)
	b, err := e.BeginArtifact(j2.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", data, 0}})})
	if err != nil || !b.Files[0].Done {
		t.Fatalf("declared %+v %v", b, err)
	}
	// Simulate the race: the first artifact goes, a long time passes
	// without a commit, and the pruner claims the content.
	if _, err := e.DeleteArtifact(id, s.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE artifacts SET status = 'failed' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	clk.Add(2 * time.Hour)
	e.pruneArtifacts()
	if _, err := e.db.Exec(`UPDATE artifacts SET status = 'uploading' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.CommitArtifact(context.Background(), j2.LeaseID, b.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("commit after the content went: %v", err)
	}
	again, err := e.BeginArtifact(j2.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", data, 0}})})
	if err != nil || again.Files[0].Done {
		t.Fatalf("asked again: %+v %v", again, err)
	}
	sendAll(t, e, j2.LeaseID, again, []upFile{{"bin/app", data, 0}})
	if _, err := e.CommitArtifact(context.Background(), j2.LeaseID, b.ID); err != nil {
		t.Fatal(err)
	}
}

// Servers that do not share one store: the second finds the first's
// marker missing from its own, and keeps no artifacts rather than keeping
// them where the others cannot find them.
func TestServersMustShareTheStore(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a := openStore(t, addr, clk, t.TempDir(), ArtifactOptions{})
	b := openStore(t, addr, clk, t.TempDir(), ArtifactOptions{})
	if a.ArtifactOptions().Store == nil {
		t.Fatal("the first server turned artifacts off")
	}
	if b.ArtifactOptions().Store != nil || !strings.Contains(b.ArtifactOptions().StoreProblem, "not the one the other servers use") {
		t.Fatalf("the second server: %q", b.ArtifactOptions().StoreProblem)
	}
	// A third that shares the first's directory is fine.
	c := openStore(t, addr, clk, a.ArtifactOptions().Store.(*blobstore.Filesystem).Dir(), ArtifactOptions{})
	if c.ArtifactOptions().Store == nil {
		t.Fatalf("a server sharing the store: %q", c.ArtifactOptions().StoreProblem)
	}
}
