package work_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/work"
)

type txDB struct{ *engine.Database }

func (d txDB) BeginTx(ctx context.Context) (work.Tx, error) { return d.Begin(ctx) }

func newStore(t *testing.T) *work.Store {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return work.New(txDB{e.Database()})
}

var proj = work.Scope{Kind: work.ScopeProject, ID: "acme/shop"}

// person acts as a person with the given roles on every scope.
func person(name string, maintainer, write bool) work.Caller {
	all := func(work.Scope) bool { return true }
	return work.Caller{Actor: work.Actor{Name: name, Kind: work.KindPerson, Via: "api"}, Read: all,
		Maintain: func(work.Scope) bool { return maintainer }, Write: func(work.Scope) bool { return write }}
}

var (
	ada   = person("ada", true, true)
	bob   = person("bob", false, true)
	carol = person("carol", false, false) // may read only
)

func mustCreate(t *testing.T, s *work.Store, c work.Caller, in work.NewItem) *work.Item {
	t.Helper()
	if in.Scope.Kind == "" {
		in.Scope = proj
	}
	it, err := s.Create(context.Background(), c, in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Title, err)
	}
	return it
}

func ptr[T any](v T) *T { return &v }

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("want an error containing %q, got %v", sub, err)
	}
}

var epoch = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// clock returns a settable clock for a store.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func withClock(s *work.Store) *clock {
	c := &clock{t: epoch}
	s.SetClock(c.now)
	return c
}
