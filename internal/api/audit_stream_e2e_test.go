package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/fakeidp"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

// Every audit entry written to the database is also a line of the stream, in
// the same order, with the stable field names, and no session id, token or
// canary appears on a line.
func TestAuditStreamMatchesTheDatabase(t *testing.T) {
	e := setupAuth(t, envOpts{})
	var out lockedBuffer
	e.eng.SetAuditStream(&out, "")
	const canary = "CANARY-audit-stream-token-5d2c8b1f9a"
	secrets := []string{canary}

	otto := e.signIn(t, "otto")
	secrets = append(secrets, otto.cookieValue(e.srv.URL, sessionCookie))
	ada := e.signIn(t, "ada")
	r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	var run engine.RunSummary
	r.json(t, &run)
	otto.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+run.ID+"/cancel", `{}`)
	id, tok := ada.newToken(e.srv.URL, "nightly")
	secrets = append(secrets, tok)
	ada.do(http.MethodDelete, e.srv.URL+"/api/v1/tokens/"+id, "")
	// A token that is not one: refused, and its text must not be echoed.
	resp := newBrowser(t).do(http.MethodPost, e.srv.URL+"/api/v1/projects/web/runs", `{"ref":"main"}`, bearer(canary)...)
	_ = resp
	e.idp.Fail(fakeidp.Expired)
	newBrowser(t).login(e.srv.URL, "/")
	e.idp.Fail(fakeidp.None)
	otto.do(http.MethodPost, e.srv.URL+"/auth/logout", "")

	db := e.auditEntries(t)
	var lines []engine.AuditLine
	sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("not JSON: %q", sc.Text())
		}
		for _, f := range []string{"time", "kind", "actor", "actor_kind", "organization", "project", "target", "outcome", "detail", "source_addr", "request_id"} {
			if _, ok := raw[f]; !ok {
				t.Fatalf("line without %q: %s", f, sc.Text())
			}
		}
		var l engine.AuditLine
		_ = json.Unmarshal(sc.Bytes(), &l)
		lines = append(lines, l)
	}
	if len(lines) != len(db) || len(db) < 8 {
		t.Fatalf("%d lines for %d database entries", len(lines), len(db))
	}
	for i, a := range db {
		l := lines[i]
		want := a.User
		if want == "" {
			want = "system"
		}
		if l.Kind != a.Action || l.Actor != want || l.Outcome != a.Outcome || l.Project != a.Project || l.SourceAddr != a.Source {
			t.Errorf("line %d %+v does not match entry %+v", i, l, a)
		}
		if l.RequestID == "" {
			t.Errorf("line %d (%s) has no request id", i, l.Kind)
		}
	}
	for _, s := range secrets {
		if s == "" || strings.Contains(string(out.Bytes()), s) {
			t.Fatalf("a session id, token or canary (%d chars) is on the audit stream", len(s))
		}
	}
}

func TestRequestIDIsEchoedOrMade(t *testing.T) {
	e := setupAuth(t, envOpts{})
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-Id", "caller-chosen-id-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "caller-chosen-id-1" {
		t.Errorf("echoed %q", got)
	}
	req.Header.Set("X-Request-Id", "bad id with spaces")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); !strings.HasPrefix(got, "req_") {
		t.Errorf("made %q", got)
	}
}
