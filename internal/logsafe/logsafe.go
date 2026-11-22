// Package logsafe wraps a log handler so that nothing a person or a remote
// peer typed can forge a log line. Control characters (a line break above all)
// in the message and in every text attribute are written as visible escapes
// (\n, \r, \x1b ...) before the wrapped handler sees them, whatever the
// handler's format: the text handler quotes such values on its own, the JSON
// handler escapes them, but a line-based collector or a custom handler
// might not, and the guarantee is cheaper kept in one place than assumed at
// every call.
package logsafe

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Handler returns h with control characters neutralised in what it logs.
func Handler(h slog.Handler) slog.Handler { return &handler{next: h} }

type handler struct{ next slog.Handler }

// Clean writes control characters of s as escapes and invalid UTF-8 as
// \xNN; other text is returned as it was.
func Clean(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == utf8.RuneError }) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r):
			if r < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func clean(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Clean(a.Value.String()))
	case slog.KindGroup:
		gs := a.Value.Group()
		out := make([]slog.Attr, len(gs))
		for i, g := range gs {
			out[i] = clean(g)
		}
		a.Value = slog.GroupValue(out...)
	case slog.KindAny:
		// An error or a Stringer prints its own text: neutralise that.
		switch v := a.Value.Any().(type) {
		case error:
			a.Value = slog.StringValue(Clean(v.Error()))
		case fmt.Stringer:
			a.Value = slog.StringValue(Clean(v.String()))
		}
	}
	a.Key = Clean(a.Key)
	return a
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	n := slog.NewRecord(r.Time, r.Level, Clean(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { n.AddAttrs(clean(a)); return true })
	return h.next.Handle(ctx, n)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = clean(a)
	}
	return &handler{next: h.next.WithAttrs(out)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(Clean(name))}
}
