package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Test attachments and hosted HTML reports are ordinary artifacts, so they
// go wherever the server keeps artifacts: with the database store (schema
// 17), which a server on PostgreSQL uses when nothing is configured, they
// are written to and read back from the database, through the API.
func TestReportsInTheDatabaseStore(t *testing.T) {
	cases := []struct {
		name string
		opts func(t *testing.T) engine.ArtifactOptions
		want string
	}{
		{"configured", func(*testing.T) engine.ArtifactOptions { return engine.ArtifactOptions{Choice: engine.StoreDatabase} }, "database"},
		// Nothing configured: the database on PostgreSQL, the
		// filesystem on SQLite.
		{"default", func(t *testing.T) engine.ArtifactOptions {
			return engine.ArtifactOptions{Choice: engine.StoreAuto, Store: mustFS(t)}
		}, map[bool]string{true: "database", false: "filesystem"}[testdb.Postgres()]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: 5 * time.Second, Logger: quiet(), Artifacts: tc.opts(t)})
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
			srv := httptest.NewServer(s.Handler())
			t.Cleanup(func() { srv.Close(); eng.Close() })

			run, caseID := runWithReports(t, eng, "app")
			st, err := eng.StoreStatus(context.Background())
			if err != nil || st.Store != tc.want || !st.Available {
				t.Fatalf("store %+v %v", st, err)
			}
			// Eight files: six attachments and the report's two pages.
			if st.Kept.Store != tc.want || st.Kept.Files != 8 || len(st.Elsewhere) != 0 {
				t.Fatalf("kept %+v elsewhere %+v", st.Kept, st.Elsewhere)
			}
			if d, err := eng.GetRun(run); err != nil || d.MinSchema != 18 {
				t.Fatalf("min_schema %d %v", d.MinSchema, err)
			}

			get := func(path string) (int, []byte) {
				t.Helper()
				resp, err := http.Get(srv.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, b
			}
			// A test attachment, read back whole.
			if code, b := get(fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d/attachments/0", run, caseID)); code != http.StatusOK || string(b) != string(tinyPNG) {
				t.Fatalf("attachment: %d %q", code, b)
			}
			// The hosted HTML report, by its signed link, and a file beside it.
			var list reportList
			resp, err := http.Get(srv.URL + "/api/v1/runs/" + run + "/reports")
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			if err != nil || len(list.Reports) != 1 {
				t.Fatalf("reports %+v %v", list, err)
			}
			if code, b := get(list.Reports[0].URL); code != http.StatusOK || string(b) != string(withStorageShim([]byte(hostileReport))) {
				t.Fatalf("hosted page: %d %s", code, b)
			}
			if code, b := get(list.Reports[0].Base + "app.js"); code != http.StatusOK || string(b) != "console.log('report')" {
				t.Fatalf("hosted app.js: %d %s", code, b)
			}
		})
	}
}
