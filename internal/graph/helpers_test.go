package graph

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// newStore opens a test database (SQLite, or PostgreSQL when
// LAUNCHWAY_TEST_POSTGRES is set) and a store with a clock that moves one
// second at each call.
func newStore(t testing.TB) *Store {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	s := NewStore(e.Database())
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { at = at.Add(time.Second); return at }
	return s
}

func ref(k NodeKind, key string) NodeRef { return NodeRef{k, key} }

func repoN(path string) Node {
	return Node{Kind: KindRepository, Key: path, Subject: RepoSubject(path)}
}

var bg = context.Background()
