package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func openForBases(t *testing.T, addr string, multi bool) *engine.Engine {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: addr, MultiBase: multi, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// With the switch off there is one Base and a second cannot be made; with it
// on the console makes, lists, suspends and resumes Bases, and says plainly
// what it did.
func TestBasesCommand(t *testing.T) {
	addr := testdb.New(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	run := func(e *engine.Engine, sub string, words ...string) (string, error) {
		var out bytes.Buffer
		err := runBases(e, sub, words, "", "", &out, now)
		return out.String(), err
	}

	off := openForBases(t, addr, false)
	out, err := run(off, "list")
	if err != nil || !strings.Contains(out, "default") || !strings.Contains(out, "One Base") {
		t.Fatalf("list with the switch off: %q %v", out, err)
	}
	if _, err := run(off, "create", "acme"); err == nil || !strings.Contains(err.Error(), "multi_base") {
		t.Fatalf("a second Base with the switch off: %v", err)
	}
	off.Close()

	on := openForBases(t, addr, true)
	if out, err := run(on, "create", "acme"); err != nil || !strings.Contains(out, "acme") {
		t.Fatalf("create: %q %v", out, err)
	}
	if _, err := run(on, "create", "acme"); err == nil {
		t.Fatal("a label taken twice")
	}
	if _, err := run(on, "create", "www"); err == nil {
		t.Fatal("a reserved label")
	}
	out, _ = run(on, "list")
	if !strings.Contains(out, "acme") || !strings.Contains(out, "default") || strings.Contains(out, "One Base") {
		t.Fatalf("list: %q", out)
	}
	if out, err := run(on, "suspend", "acme"); err != nil || !strings.Contains(out, "suspended") {
		t.Fatalf("suspend: %q %v", out, err)
	}
	if _, err := run(on, "suspend", "default"); err == nil {
		t.Fatal("the default Base was suspended")
	}
	if out, err := run(on, "resume", "acme"); err != nil || !strings.Contains(out, "active") {
		t.Fatalf("resume: %q %v", out, err)
	}
	if _, err := run(on, "resume", "nobody"); err == nil {
		t.Fatal("a Base that does not exist was resumed")
	}
}
