package logsafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// rawHandler writes the message and every attribute value unquoted, one
// line per record, as a naive collector would: only the wrapper stands
// between a value's line break and a forged line.
type rawHandler struct {
	buf   *bytes.Buffer
	attrs []slog.Attr
}

func (rawHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h rawHandler) Handle(_ context.Context, r slog.Record) error {
	fmt.Fprint(h.buf, r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(h.buf, " %s=%s", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool { fmt.Fprintf(h.buf, " %s=%s", a.Key, a.Value); return true })
	h.buf.WriteString("\n")
	return nil
}
func (h rawHandler) WithAttrs(as []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return h
}
func (h rawHandler) WithGroup(string) slog.Handler { return h }

func TestNoForgedLines(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(Handler(rawHandler{buf: &buf})).With("who", "a\nINFO forged")
	l.Info("msg\r\nINFO forged", "name", "x\ny", "err", errors.New("bad\nline"),
		slog.Group("g", "k", "v\x1b[31m"), "bin", "\xff")
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("a value forged a line:\n%q", out)
	}
	for _, want := range []string{`msg\r\nINFO forged`, `name=x\ny`, `err=bad\nline`, `\x1b[31m`, `\xff`, `who=a\nINFO forged`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestCleanLeavesPlainTextAlone(t *testing.T) {
	for _, s := range []string{"", "plain text", "ünïcode ✓", `quote " and \ backslash`} {
		if Clean(s) != s {
			t.Errorf("Clean(%q) changed it", s)
		}
	}
}
