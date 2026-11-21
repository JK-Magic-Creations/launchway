package engine

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/redact"
)

// The audit stream: every entry written to the audit table is also written,
// after it, as one JSON line to a writer (the server's standard output), so a
// log collector can alert on sign-in failures without polling the API. The one
// place both happen is Engine.Audit, so no kind can be recorded without being
// emitted. The stream never holds a secret value: addresses lose their
// credentials, bearer tokens and secret-looking query or assignment values
// are masked, and the secret APIs record names only.

// AuditLine is one line of the stream. The field names are a contract.
type AuditLine struct {
	Time         string `json:"time"`         // UTC, RFC 3339 with milliseconds
	Kind         string `json:"kind"`         // such as auth.sign_in or repository.delete
	Actor        string `json:"actor"`        // who; "system" when no person acted
	ActorKind    string `json:"actor_kind"`   // person, runner or system
	Organization string `json:"organization"` // the organization the entry belongs to
	Project      string `json:"project"`      // "" when the entry is about no project
	Target       string `json:"target"`       // what was acted on
	Outcome      string `json:"outcome"`      // ok, denied or failed
	Detail       string `json:"detail"`       // a short note, redacted
	SourceAddr   string `json:"source_addr"`  // the client's address
	RequestID    string `json:"request_id"`   // matches the X-Request-Id response header
}

type auditStream struct {
	mu  sync.Mutex
	w   io.Writer
	org string
}

// auditStreams holds each engine's stream, beside the other per-engine hooks.
var auditStreams sync.Map // *Engine -> *auditStream

// SetAuditStream turns the stream on, writing to w (nil turns it off). org is
// the organization name the lines carry.
func (e *Engine) SetAuditStream(w io.Writer, org string) {
	if w == nil {
		auditStreams.Delete(e)
		return
	}
	if org == "" {
		org = "default"
	}
	auditStreams.Store(e, &auditStream{w: w, org: org})
}

var (
	bearerText = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]{8,}`)
	assignText = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|authorization)(\s*[=:]\s*)[^\s,;&"']+`)
)

func scrubAudit(s string) string {
	s = redact.URLs(s)
	s = bearerText.ReplaceAllString(s, "$1 ***")
	return assignText.ReplaceAllString(s, "$1$2***")
}

// ActorKind says what kind of caller a recorded user name is.
func ActorKind(user string) string {
	switch {
	case strings.HasPrefix(user, "runner:"):
		return "runner"
	case user == "" || user == "system":
		return "system"
	}
	return "person"
}

// auditLineOf builds the stream line of an entry.
func auditLineOf(a AuditEntry, org string) AuditLine {
	actor := a.User
	if actor == "" {
		actor = "system"
	}
	return AuditLine{
		Time: time.UnixMilli(a.At).UTC().Format("2006-01-02T15:04:05.000Z07:00"), Kind: a.Action,
		Actor: scrubAudit(actor), ActorKind: ActorKind(a.User), Organization: org, Project: a.Project,
		Target: scrubAudit(a.Target), Outcome: a.Outcome, Detail: scrubAudit(a.Detail),
		SourceAddr: a.Source, RequestID: a.RequestID,
	}
}

func (e *Engine) emitAudit(a AuditEntry) {
	v, ok := auditStreams.Load(e)
	if !ok {
		return
	}
	st := v.(*auditStream)
	b, err := json.Marshal(auditLineOf(a, st.org))
	if err != nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	_, _ = st.w.Write(append(b, '\n')) // a full pipe must not fail the action: the database has the record
}
