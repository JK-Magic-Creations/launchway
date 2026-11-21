package files

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	gitexec.Reexec()
	os.Exit(m.Run())
}

const repoID = "rep1"

func newService(t *testing.T) (*Service, *storage.Store) {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.New(context.Background(), storage.Options{DB: eng.Database(), Blobs: blobs, CacheDir: filepath.Join(t.TempDir(), "repos"), Server: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), repoID); err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	return &Service{Git: st, Clock: func() time.Time { clock = clock.Add(time.Minute); return clock }}, st
}

// seed makes the first commit of a branch with some files, server-side.
func seed(t *testing.T, s *Service, st *storage.Store, branch string, files map[string]string) {
	t.Helper()
	_, err := st.Mutate(context.Background(), repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		g := runner{dir: scratch, limits: st.Limits(), scratch: true, env: []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "i")}}
		for p, c := range files {
			blob, err := g.run(ctx, []byte(c), "hash-object", "-w", "--stdin")
			if err != nil {
				return nil, err
			}
			if _, err := g.run(ctx, []byte("100644 "+strings.TrimSpace(blob)+"\t"+p+"\n"), "update-index", "--add", "--index-info"); err != nil {
				return nil, err
			}
		}
		tree, err := g.run(ctx, nil, "write-tree")
		if err != nil {
			return nil, err
		}
		g.env = append(g.env, identEnv("seed", s.now())...)
		c, err := g.run(ctx, nil, "commit-tree", strings.TrimSpace(tree), "-m", "first\n\nbody line")
		if err != nil {
			return nil, err
		}
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, New: strings.TrimSpace(c)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadCommitsAndPut(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n", "openspec/changes/a/spec.md": "# a\n", "bin.dat": "\x00\x01\xff"})

	root, err := s.Read(ctx, repoID, "main", "")
	if err != nil || root.Type != "dir" || len(root.Entries) != 3 {
		t.Fatalf("%+v %v", root, err)
	}
	dir, err := s.Read(ctx, repoID, "main", "openspec/changes")
	if err != nil || len(dir.Entries) != 1 || dir.Entries[0].Name != "a" || dir.Entries[0].Type != "dir" || dir.Entries[0].Path != "openspec/changes/a" {
		t.Fatalf("%+v %v", dir, err)
	}
	f, err := s.Read(ctx, repoID, "main", "README.md")
	if err != nil || f.Type != "file" || f.Encoding != "utf8" || f.Content != "hello\n" || f.Size != 6 || len(f.SHA) != 40 {
		t.Fatalf("%+v %v", f, err)
	}
	if b, _ := s.Read(ctx, repoID, "main", "bin.dat"); b.Encoding != "base64" || b.Content != base64.StdEncoding.EncodeToString([]byte("\x00\x01\xff")) {
		t.Fatalf("binary: %+v", b)
	}
	for _, bad := range []string{"../etc/passwd", "a//b", ".git/config", "a/./b"} {
		if _, err := s.Read(ctx, repoID, "main", bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := s.Read(ctx, repoID, "main", "nope.txt"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	for _, ref := range []string{"--all", "-x", "nope", "a..b"} {
		if _, err := s.Read(ctx, repoID, ref, "README.md"); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}

	// Create, refuse to overwrite blind, replace with the id, refuse a stale id.
	w, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "openspec/changes/b/spec.md", Content: []byte("# b\n"), Message: "file spec b", Actor: "brain"}, nil)
	if err != nil || len(w.Commit) != 40 || w.Branch != "main" {
		t.Fatalf("create: %+v %v", w, err)
	}
	cs, err := s.Commits(ctx, repoID, "main", "", 10)
	if err != nil || len(cs) != 2 || cs[0].SHA != w.Commit || cs[0].Subject != "file spec b" || cs[0].Author != "brain" || cs[1].Message != "first\n\nbody line" || cs[0].Parents != 1 {
		t.Fatalf("log: %+v %v", cs, err)
	}
	if cs, _ = s.Commits(ctx, repoID, "main", "openspec/changes/b", 10); len(cs) != 1 {
		t.Fatalf("log of a path: %+v", cs)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("x"), Message: "m"}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("a blind overwrite: %v", err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("changed\n"), Message: "m", ExpectedSHA: f.SHA}, nil); err != nil {
		t.Errorf("replace: %v", err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("again\n"), Message: "m", ExpectedSHA: f.SHA}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("a stale id: %v", err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md/x", Content: []byte("x"), Message: "m"}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("a file where a directory is needed: %v", err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "gone", Path: "x", Content: []byte("x"), Message: "m"}, nil); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a missing branch: %v", err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "x", Content: []byte("x"), Message: " "}, nil); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("no message: %v", err)
	}
	// The branch rules (the caller's check) can refuse, and then nothing changed.
	before, _ := s.Commits(ctx, repoID, "main", "", 50)
	deny := func(ref, _, _ string) error { return errors.New("branch is protected") }
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "new.txt", Content: []byte("x"), Message: "m"}, deny); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("rule: %v", err)
	}
	if after, _ := s.Commits(ctx, repoID, "main", "", 50); len(after) != len(before) {
		t.Errorf("a refused write moved the branch")
	}
	// The repository is sound.
	if err := st.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: st.Limits()}
		_, err := g.run(ctx, nil, "fsck", "--strict", "--no-dangling")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLargeFilesAreNotReadWhole(t *testing.T) {
	s, st := newService(t)
	seed(t, s, st, "main", map[string]string{"big.txt": strings.Repeat("a", MaxFileBytes+1)})
	f, err := s.Read(context.Background(), repoID, "main", "big.txt")
	if err != nil || !f.Truncated || f.Content != "" || f.Size != MaxFileBytes+1 {
		t.Fatalf("%+v %v", f, err)
	}
}
