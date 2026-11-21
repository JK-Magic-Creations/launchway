package installer

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Transcript records what the installer said and was told, for support and for the person's own
// memory, without any secret. Registered secrets and anything shaped like a one-time code or a
// token are replaced before a line is written.
type Transcript struct {
	mu      sync.Mutex
	w       io.Writer
	secrets []string
	now     func() time.Time
}

// NewTranscript writes to w (nil: records nothing).
func NewTranscript(w io.Writer, now func() time.Time) *Transcript {
	if now == nil {
		now = time.Now
	}
	return &Transcript{w: w, now: now}
}

var (
	codeShape   = regexp.MustCompile(`\b[A-Z0-9]{5}(-[A-Z0-9]{5}){4}\b`)
	bearerShape = regexp.MustCompile(`(?i)(bearer|token[=:]|password[=:]|passphrase[=:])\s*\S+`)
	urlUserPass = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s]+@`)
)

// Hide registers a value that must never reach the transcript.
func (t *Transcript) Hide(v string) {
	if t == nil || len(v) < 4 {
		return
	}
	t.mu.Lock()
	t.secrets = append(t.secrets, v)
	t.mu.Unlock()
}

// Redact returns s with secrets removed.
func (t *Transcript) Redact(s string) string {
	if t != nil {
		t.mu.Lock()
		for _, v := range t.secrets {
			s = strings.ReplaceAll(s, v, "[hidden]")
		}
		t.mu.Unlock()
	}
	s = codeShape.ReplaceAllString(s, "[one-time code hidden]")
	s = urlUserPass.ReplaceAllString(s, "${1}[hidden]@")
	return bearerShape.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.IndexAny(m, " =:")
		return m[:i+1] + " [hidden]"
	})
}

// Line writes one line.
func (t *Transcript) Line(format string, a ...any) {
	if t == nil || t.w == nil {
		return
	}
	line := t.Redact(fmt.Sprintf(format, a...))
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.w, "%s  %s\n", t.now().UTC().Format("15:04:05"), strings.TrimRight(line, "\n"))
}

// Answer records a question and its answer (a secret's value is never written).
func (t *Transcript) Answer(q Question, v, how string) {
	if t == nil {
		return
	}
	if q.Secret {
		v = "[hidden]"
	}
	t.Line("answer %s = %s (%s)", q.Key, v, how)
}

// Tee returns a writer that sends everything to out and the redacted text to the transcript.
func (t *Transcript) Tee(out io.Writer) io.Writer { return &tee{out: out, t: t} }

type tee struct {
	out io.Writer
	t   *Transcript
	buf []byte
}

func (w *tee) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(w.buf[:i]), "\r"); strings.TrimSpace(line) != "" {
			w.t.Line("%s", line)
		}
		w.buf = w.buf[i+1:]
	}
	return n, err
}
