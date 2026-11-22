package engine

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// freeAddr returns a local address that nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// A server started before its database keeps trying, saying so, and
// starts once the database answers.
func TestOpenWaitsForTheDatabase(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	u, err := url.Parse(testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	db, late := u.Host, freeAddr(t)
	u.Host = late

	// The database "appears" at late after a while: a proxy to the real one.
	var ln net.Listener
	var lnMu sync.Mutex
	go func() {
		time.Sleep(1500 * time.Millisecond)
		l, err := net.Listen("tcp", late)
		if err != nil {
			t.Error(err)
			return
		}
		lnMu.Lock()
		ln = l
		lnMu.Unlock()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", db)
				if err != nil {
					return
				}
				defer up.Close()
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	t.Cleanup(func() {
		lnMu.Lock()
		defer lnMu.Unlock()
		if ln != nil {
			ln.Close()
		}
	})

	var logs syncBuffer
	start := time.Now()
	e, err := Open(Options{Database: u.String(), ConnectWait: 30 * time.Second, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatalf("%v\n%s", err, logs.String())
	}
	defer e.Close()
	if took := time.Since(start); took < time.Second {
		t.Fatalf("opened after %s, before the database was there", took)
	}
	if n := strings.Count(logs.String(), "the database does not answer yet"); n < 2 {
		t.Fatalf("%d attempts logged:\n%s", n, logs.String())
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanOut), Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
		t.Fatal(err)
	}
}

// A database that never answers is given up on after ConnectWait.
func TestOpenGivesUpOnTheDatabase(t *testing.T) {
	start := time.Now()
	_, err := Open(Options{Database: "postgres://u:p@" + freeAddr(t) + "/db?connect_timeout=2", ConnectWait: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "connecting to the database") {
		t.Fatalf("got %v", err)
	}
	if took := time.Since(start); took < time.Second || took > testlib.Slow(10*time.Second) {
		t.Fatalf("gave up after %s, want about 1s", took)
	}
}
