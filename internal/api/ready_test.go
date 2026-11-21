package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func getReady(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// /readyz says whether the database is writable, not only reachable: a
// standby or read-only database is not ready.
func TestReadyzReportsWritableNotOnlyReachable(t *testing.T) {
	e := setup(t, time.Second)
	code, out := getReady(t, e.srv.URL)
	db, _ := out["database"].(map[string]any)
	if code != 200 || out["status"] != "ok" || db["writable"] != true {
		t.Fatalf("%d %v", code, out)
	}
	s := &Server{Engine: e.eng, RunnerToken: "secret", Log: quiet()}
	s.dbState = func(context.Context) (engine.DatabaseState, error) {
		return engine.DatabaseState{Kind: "postgresql", Standby: true, Writable: false}, nil
	}
	rec := newRecorder()
	s.readyz(rec.ResponseRecorder, requestFor("/readyz"))
	rec.code = rec.Code
	_ = json.Unmarshal(rec.Body.Bytes(), &rec.body)
	if rec.code != 503 || rec.body["status"] != "read_only" {
		t.Fatalf("a standby must not be ready: %d %v", rec.code, rec.body)
	}
	if db, _ := rec.body["database"].(map[string]any); db["standby"] != true {
		t.Fatalf("the answer should say it is a standby: %v", rec.body)
	}
}
