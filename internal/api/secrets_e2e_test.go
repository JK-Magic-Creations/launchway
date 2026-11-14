package api

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The values the tests hide. They appear nowhere in the pipelines.
const (
	fileSecret = "e2e-Secr3t-From-File-91"
	envSecret  = "e2e-Secr3t-From-Env-77"
)

// flakyStore is unavailable for its first few lookups, like a sealed Vault
// that is then unsealed.
type flakyStore struct {
	mu       sync.Mutex
	failures int
}

func (f *flakyStore) Get(context.Context, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return "", &secrets.UnavailableError{Reason: "vault is sealed"}
	}
	return fileSecret, nil
}

// syncBuffer collects the runner's own log output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSecretsEndToEnd(t *testing.T) {
	e := setup(t, 5*time.Second)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "robot-password"), []byte(fileSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LW_E2E_FROM_ENV", envSecret)
	stores := secrets.NewResolver().AddLocalEnv()
	stores.Add("files", secrets.File{Dir: dir}, nil, nil)
	stores.Add("locked", secrets.File{Dir: dir}, []string{"payments"}, nil)
	stores.Add("flaky", &flakyStore{failures: 2}, nil, nil)
	var runnerLog syncBuffer
	r := e.newRunner("r1")
	r.Secrets = stores
	r.SecretWait = time.Millisecond // hand the step back almost at once
	r.Log = slog.New(slog.NewTextHandler(&runnerLog, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stop, done := e.startRunner(t, r)

	// A step that prints its secrets, alone and inside other text, and
	// writes one to its outputs; the next step receives the masked output.
	used := e.start(t, `
pipeline: demo
steps:
  use:
    image: none
    secrets:
      TOKEN:    { store: files, key: robot-password }
      FROM_ENV: { store: env, key: LW_E2E_FROM_ENV }
    run: |
      echo "$TOKEN"
      echo "login --password=$TOKEN --user=robot"
      echo "from env: $FROM_ENV." >&2
      test ${#TOKEN} -eq 23
      echo "token=$TOKEN" >> "$LAUNCHWAY_OUTPUTS"
  next:
    image: none
    needs: use
    run: test "$LAUNCHWAY_NEEDS_USE_TOKEN" = '***'
`)
	missing := e.start(t, "pipeline: demo\nsteps:\n  a:\n    image: none\n    secrets: { TOKEN: { store: files, key: no-such-file } }\n    run: echo should not run\n")
	refused := e.start(t, "pipeline: demo\nsteps:\n  a:\n    image: none\n    secrets: { TOKEN: { store: locked, key: robot-password } }\n    run: echo should not run\n")
	flaky := e.start(t, "pipeline: demo\nsteps:\n  a:\n    image: none\n    secrets: { TOKEN: { store: flaky, key: k } }\n    run: echo \"got $TOKEN\"\n")

	d := e.wait(t, used, 20*time.Second)
	if d.Status != "succeeded" {
		t.Fatalf("run %s: %+v", d.Status, d.StepList)
	}
	if got := d.StepList[0].Outputs["token"]; got != "***" {
		t.Fatalf("output token = %q, want it masked", got)
	}
	logs := stepLogs(t, e, used, "use", 1)
	for _, want := range []string{"stdout: ***\n", "stdout: login --password=*** --user=robot\n", "stderr: from env: ***.\n",
		"system: Secret TOKEN ← files:robot-password\n", "system: Secret FROM_ENV ← env:LW_E2E_FROM_ENV\n"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the log lacks %q:\n%s", want, logs)
		}
	}
	// The run page shows the references.
	if ref := d.StepList[0].Secrets["TOKEN"]; ref.Store != "files" || ref.Key != "robot-password" {
		t.Fatalf("step view secrets %+v", d.StepList[0].Secrets)
	}

	d = e.wait(t, missing, 20*time.Second)
	wantStepError(t, d, `secret TOKEN (files:no-such-file): there is no file "no-such-file"`)
	if l := stepLogs(t, e, missing, "a", 1); strings.Contains(l, "should not run") {
		t.Fatal("the script ran without its secret")
	}
	d = e.wait(t, refused, 20*time.Second)
	wantStepError(t, d, `secret TOKEN (locked:robot-password): store "locked" does not allow project "demo" (projects: payments)`)

	d = e.wait(t, flaky, 30*time.Second)
	if d.Status != "succeeded" || d.StepList[0].Attempt < 2 {
		t.Fatalf("flaky store: run %s, attempt %d, error %q", d.Status, d.StepList[0].Attempt, d.StepList[0].Error)
	}
	if l := stepLogs(t, e, flaky, "a", 1); !strings.Contains(l, "vault is sealed. The step will be run again.") {
		t.Fatalf("first attempt's log:\n%s", l)
	}
	if l := stepLogs(t, e, flaky, "a", d.StepList[0].Attempt); !strings.Contains(l, "stdout: got ***") {
		t.Fatalf("last attempt's log:\n%s", l)
	}
	var events []engine.Event
	getJSON(t, e.srv.URL+"/api/v1/runs/"+flaky+"/events", &events)
	lost := false
	for _, ev := range events {
		lost = lost || (ev.Type == engine.StepLost && strings.Contains(ev.Data.Reason, "vault is sealed"))
	}
	if !lost {
		t.Fatalf("no step.lost event naming the store's trouble: %+v", events)
	}

	stop()
	<-done

	// The values appear nowhere: not in the API's answers…
	var answers strings.Builder
	get := func(path string) {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %s", path, resp.Status)
		}
		answers.Write(b)
	}
	get("/api/v1/runs")
	for _, id := range []string{used, missing, refused, flaky} {
		get("/api/v1/runs/" + id)
		get("/api/v1/runs/" + id + "/events")
		run, _ := e.eng.GetRun(id)
		for _, s := range run.StepList {
			for a := 1; a <= s.Attempt; a++ {
				get(fmt.Sprintf("/api/v1/runs/%s/steps/%s/logs?attempt=%d", id, s.ID, a))
			}
		}
	}
	if !strings.Contains(answers.String(), "login --password=*** --user=robot") {
		t.Fatal("the answers searched do not include the logs")
	}
	// …not in the runner's own log…
	if !strings.Contains(runnerLog.String(), "step finished") {
		t.Fatalf("the runner's log was not captured:\n%s", runnerLog.String())
	}
	// …and not in any table of the database.
	dump := dumpDatabase(t, e.db)
	if !strings.Contains(dump, "Secret TOKEN ← files:robot-password") {
		t.Fatal("the database dump does not include the logs")
	}
	for _, v := range []string{fileSecret, envSecret} {
		for where, text := range map[string]string{"the API": answers.String(), "the runner's log": runnerLog.String(), "the database": dump} {
			if strings.Contains(text, v) {
				t.Errorf("a secret value appears in %s", where)
			}
		}
	}
}

func wantStepError(t *testing.T, d *engine.RunDetail, want string) {
	t.Helper()
	if d.Status != "failed" || d.StepList[0].Status != "failed" || d.StepList[0].Error != want {
		t.Fatalf("run %s, step %s, error %q; want failed with %q", d.Status, d.StepList[0].Status, d.StepList[0].Error, want)
	}
}

func stepLogs(t *testing.T, e *env, runID, step string, attempt int) string {
	t.Helper()
	var logs struct {
		Lines []engine.LogLine `json:"lines"`
	}
	getJSON(t, fmt.Sprintf("%s/api/v1/runs/%s/steps/%s/logs?attempt=%d", e.srv.URL, runID, step, attempt), &logs)
	var b strings.Builder
	for _, l := range logs.Lines {
		b.WriteString(l.Stream + ": " + l.Text + "\n")
	}
	return b.String()
}

// dumpDatabase returns the text of every row of every table.
func dumpDatabase(t *testing.T, addr string) string {
	t.Helper()
	driver, dsn, list := "sqlite", "file:"+addr, `SELECT name FROM sqlite_master WHERE type = 'table'`
	if testdb.Postgres() {
		driver, dsn, list = "pgx", addr, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables []string
	rows, err := db.Query(list)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < 5 {
		t.Fatalf("found only these tables: %v", tables)
	}
	var b strings.Builder
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&b, "%v\t", v)
			}
			b.WriteString("\n")
		}
		rows.Close()
	}
	return b.String()
}
