package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// A runner looks up, saves and restores an entry over HTTP under its lease;
// the request says no scope (one it adds is ignored), and the server's
// answers say where the entry went. People list and delete it, and the
// policy is read and changed, all through the API.
func TestCacheRoutes(t *testing.T) {
	e := setupStore(t)
	p, err := pipeline.Parse([]byte("pipeline: demo\non:\n  pull_request: {}\nsteps:\n  build:\n    image: x\n    run: make\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Steps[0].Cache = []*pipeline.Cache{{Name: "pip", Paths: []string{".cache"}, Key: pipeline.CacheKey{Text: "v1"}}}
	lease := func(trig engine.Trigger) string {
		t.Helper()
		if _, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: trig, Source: engine.Source{Kind: "dir", Path: "/tmp"}}); err != nil {
			t.Fatal(err)
		}
		j, err := e.eng.Lease(t.Context(), engine.RunnerInfo{ID: "r1", Name: "r1", Features: engine.FeatureArtifacts + " " + engine.FeatureCache}, 0)
		if err != nil || j == nil {
			t.Fatalf("%+v %v", j, err)
		}
		if _, err := e.eng.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		return j.LeaseID
	}
	call := func(method, path, token string, body any, raw []byte) (int, []byte, http.Header) {
		t.Helper()
		var rd io.Reader
		if raw != nil {
			rd = bytes.NewReader(raw)
		} else if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.Header.Set(csrfHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out, resp.Header
	}
	main := engine.Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: "a"}
	l1 := lease(main)
	key := hexSum([]byte("lock"))
	data := bytes.Repeat([]byte("cache bytes "), 5000)

	// Nothing yet.
	st, body, _ := call("POST", "/api/runner/v1/caches/lookup", "secret", map[string]any{"lease_id": l1, "name": "pip", "key": key, "platform": "linux/amd64"}, nil)
	if st != 200 || strings.Contains(string(body), `"entry"`) {
		t.Fatalf("lookup: %d %s", st, body)
	}
	// Without the runner token, or for a lease that is not live: refused.
	if st, _, _ := call("POST", "/api/runner/v1/caches/lookup", "", map[string]any{"lease_id": l1}, nil); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	if st, _, _ := call("POST", "/api/runner/v1/caches/lookup", "secret", map[string]any{"lease_id": "lease_nope", "name": "pip", "key": key, "platform": "linux/amd64"}, nil); st != 410 {
		t.Fatalf("a lease that is gone: %d", st)
	}
	// A scope in the request is not a thing: it is not read.
	st, body, _ = call("POST", "/api/runner/v1/caches/begin", "secret", map[string]any{"lease_id": l1, "name": "pip", "key": key, "platform": "linux/amd64",
		"bytes": len(data), "files": 3, "sha256": hexSum(data), "scope": "default", "project": "other"}, nil)
	var begun engine.BegunCache
	if json.Unmarshal(body, &begun); st != 200 || begun.ID == "" || begun.Scope != "default" || begun.Exists {
		t.Fatalf("begin: %d %s", st, body)
	}
	// A damaged chunk is refused; the real one is taken.
	q := fmt.Sprintf("/api/runner/v1/caches/chunk?lease_id=%s&cache_id=%s&offset=0&sha256=", l1, begun.ID)
	if st, _, _ := call("PUT", q+hexSum([]byte("x")), "secret", nil, data); st != 400 {
		t.Fatalf("a damaged chunk: %d", st)
	}
	st, body, _ = call("PUT", q+hexSum(data), "secret", nil, data)
	if st != 200 || !strings.Contains(string(body), fmt.Sprintf(`"received":%d`, len(data))) {
		t.Fatalf("chunk: %d %s", st, body)
	}
	st, body, _ = call("POST", "/api/runner/v1/caches/commit", "secret", map[string]any{"lease_id": l1, "cache_id": begun.ID}, nil)
	if st != 200 || !strings.Contains(string(body), `"bytes":`+fmt.Sprint(len(data))) {
		t.Fatalf("commit: %d %s", st, body)
	}
	// Saving the same key again says it exists; an over-size entry is a 413,
	// a policy that forbids is a 403.
	st, body, _ = call("POST", "/api/runner/v1/caches/begin", "secret", map[string]any{"lease_id": l1, "name": "pip", "key": key, "platform": "linux/amd64",
		"bytes": 3, "files": 1, "sha256": hexSum([]byte("abc"))}, nil)
	if st != 200 || !strings.Contains(string(body), `"exists":true`) {
		t.Fatalf("begin again: %d %s", st, body)
	}
	st, body, _ = call("POST", "/api/runner/v1/caches/begin", "secret", map[string]any{"lease_id": l1, "name": "pip", "key": hexSum([]byte("big")), "platform": "linux/amd64",
		"bytes": 3 << 30, "files": 1, "sha256": hexSum([]byte("abc"))}, nil)
	if st != 413 || !strings.Contains(string(body), "an entry may hold at most 2 GiB") {
		t.Fatalf("too large: %d %s", st, body)
	}

	// The next run restores it, from the start and from an offset.
	l2 := lease(main)
	st, body, _ = call("POST", "/api/runner/v1/caches/lookup", "secret", map[string]any{"lease_id": l2, "name": "pip", "key": key, "platform": "linux/amd64"}, nil)
	var res engine.CacheLookupResult
	if json.Unmarshal(body, &res); st != 200 || res.Entry == nil || !res.Entry.Exact || res.Entry.Scope != "default" || res.Entry.SHA256 != hexSum(data) {
		t.Fatalf("lookup: %d %s", st, body)
	}
	st, got, h := call("POST", "/api/runner/v1/caches/download", "secret", map[string]any{"lease_id": l2, "cache_id": res.Entry.ID}, nil)
	if st != 200 || !bytes.Equal(got, data) || h.Get("X-Launchway-SHA256") != hexSum(data) {
		t.Fatalf("download: %d %d bytes", st, len(got))
	}
	st, got, _ = call("POST", "/api/runner/v1/caches/download", "secret", map[string]any{"lease_id": l2, "cache_id": res.Entry.ID, "offset": 100}, nil)
	if st != 200 || !bytes.Equal(got, data[100:]) {
		t.Fatalf("download from an offset: %d %d bytes", st, len(got))
	}
	// An ID that does not exist is not found.
	if st, _, _ := call("POST", "/api/runner/v1/caches/download", "secret", map[string]any{"lease_id": l2, "cache_id": "cch_nope"}, nil); st != 404 {
		t.Fatalf("an entry that does not exist: %d", st)
	}

	// People: list, with the run that wrote it and the quota.
	st, body, _ = call("GET", "/api/v1/projects/demo/caches", "", nil, nil)
	var list engine.CacheList
	if json.Unmarshal(body, &list); st != 200 || len(list.Entries) != 1 || list.Entries[0].Hits != 1 || list.Entries[0].Scope != "default" ||
		list.Totals.Bytes != int64(len(data)) || list.Totals.QuotaBytes != 10<<30 || list.Totals.IdleDays != 7 || list.Entries[0].RunNumber == 0 {
		t.Fatalf("list: %d %s", st, body)
	}
	// Policy: read, change (the installation's, a project's), clear.
	if st, body, _ = call("GET", "/api/v1/cache-policy", "", nil, nil); st != 200 || !strings.Contains(string(body), `"source":"default"`) {
		t.Fatalf("%d %s", st, body)
	}
	if st, body, _ = call("PUT", "/api/v1/cache-policy", "", map[string]any{"idle_days": 5}, nil); st != 200 || !strings.Contains(string(body), `"idle_days":5`) || !strings.Contains(string(body), `"max_project_gib":10`) {
		t.Fatalf("%d %s", st, body)
	}
	if st, body, _ = call("PUT", "/api/v1/cache-policy", "", map[string]any{"max_entry_gib": 99}, nil); st != 400 {
		t.Fatalf("%d %s", st, body)
	}
	if st, body, _ = call("PUT", "/api/v1/projects/demo/cache-policy", "", map[string]any{"enabled": false}, nil); st != 200 || !strings.Contains(string(body), `"source":"project"`) {
		t.Fatalf("%d %s", st, body)
	}
	// With the policy off for the project, the runner gets nothing and may not save.
	l3 := lease(main)
	st, body, _ = call("POST", "/api/runner/v1/caches/lookup", "secret", map[string]any{"lease_id": l3, "name": "pip", "key": key, "platform": "linux/amd64"}, nil)
	if st != 200 || !strings.Contains(string(body), "turned off") || strings.Contains(string(body), `"entry"`) {
		t.Fatalf("%d %s", st, body)
	}
	st, body, _ = call("POST", "/api/runner/v1/caches/begin", "secret", map[string]any{"lease_id": l3, "name": "pip", "key": hexSum([]byte("n")), "platform": "linux/amd64",
		"bytes": 3, "files": 1, "sha256": hexSum([]byte("abc"))}, nil)
	if st != 403 || !strings.Contains(string(body), "turned off") {
		t.Fatalf("%d %s", st, body)
	}
	if st, _, _ = call("DELETE", "/api/v1/projects/demo/cache-policy", "", nil, nil); st != 200 {
		t.Fatalf("%d", st)
	}
	// Delete: by name (the query), then nothing is left, and it is audited.
	if st, body, _ = call("DELETE", "/api/v1/projects/demo/caches", "", nil, nil); st != 400 {
		t.Fatalf("delete without a name: %d %s", st, body)
	}
	st, body, _ = call("DELETE", "/api/v1/projects/demo/caches?name=pip", "", nil, nil)
	if st != 200 || !strings.Contains(string(body), `"ended":1`) {
		t.Fatalf("delete: %d %s", st, body)
	}
	if st, _, _ = call("DELETE", "/api/v1/projects/demo/caches/"+list.Entries[0].ID, "", nil, nil); st != 404 {
		t.Fatalf("delete again: %d", st)
	}
	entries, err := e.eng.AuditLog(engine.AuditFilter{Action: "cache.delete"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit: %v %+v", err, entries)
	}
	if st, body, _ = call("GET", "/api/v1/projects/demo/caches", "", nil, nil); st != 200 || !strings.Contains(string(body), `"entries":[]`) {
		t.Fatalf("%d %s", st, body)
	}
}

// get_run says what each step's caches did, in text an agent may read.
func TestMCPRunBriefHasTheCaches(t *testing.T) {
	d := &engine.RunDetail{StepList: []engine.StepView{{ID: "test", Status: engine.StepSucceeded, Attempt: 1, Caches: []engine.CacheResult{
		{Name: "pip", Result: engine.CacheSaved, Restored: engine.CacheFallback, Bytes: 5, Scope: "default"},
		{Name: "models", Result: engine.CacheNotSaved, Reason: "a draft run\nsaves no cache"}}}}}
	out := (&mcpCall{}).describeRun(d)
	if len(out.Steps) != 1 || len(out.Steps[0].Caches) != 2 {
		t.Fatalf("%+v", out.Steps)
	}
	c := out.Steps[0].Caches
	if c[0].Name != "pip" || c[0].Result != "saved" || c[0].Restored != "fallback" || c[0].Bytes != 5 || c[0].Scope != "default" {
		t.Errorf("%+v", c[0])
	}
	if c[1].Result != "not_saved" || !strings.Contains(c[1].Reason, "saves no cache") {
		t.Errorf("%+v", c[1])
	}
}
