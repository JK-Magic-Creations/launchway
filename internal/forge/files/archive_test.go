package files

import (
	"bytes"
	"context"
	"errors"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"io"
	"testing"
)

func TestArchiveStreamsATreeInBothFormats(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n", "src/a.go": "package a\n"})
	put(t, s, "src/b.txt", "bee\n", "", "add b")
	for _, format := range ArchiveFormats {
		var buf bytes.Buffer
		var seen ArchiveInfo
		err := s.Archive(ctx, repoID, "main", format, "shop-main", 0, func(i ArchiveInfo) (io.Writer, error) { seen = i; return &buf, nil })
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		got, err := readArchive(format, buf.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(got) != 3 || got["shop-main/README.md"] != "hello\n" || got["shop-main/src/b.txt"] != "bee\n" {
			t.Errorf("%s: %v", format, got)
		}
		if seen.Commit == "" || seen.Files != 3 || seen.Bytes != int64(len("hello\n")+len("package a\n")+len("bee\n")) {
			t.Errorf("%s: info %+v", format, seen)
		}
	}
}

func TestArchiveRefusals(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"big.txt": "0123456789012345678901234567890123456789\n"})
	never := func(ArchiveInfo) (io.Writer, error) { t.Error("a refused archive was opened"); return io.Discard, nil }
	if err := s.Archive(ctx, repoID, "main", "tar.gz", "x", 10, never); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a snapshot over the limit: %v", err)
	}
	if err := s.Archive(ctx, repoID, "nope", "zip", "x", 0, never); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("a ref that is not there: %v", err)
	}
	if err := s.Archive(ctx, repoID, "main", "rar", "x", 0, never); err == nil {
		t.Error("an unknown format")
	}
	if err := s.Archive(ctx, repoID, "main", "zip", "../x", 0, never); err == nil {
		t.Error("a folder name that climbs out")
	}
	if err := s.Archive(ctx, repoID, "main", "zip", "x", 0, func(ArchiveInfo) (io.Writer, error) { return nil, errors.New("not now") }); err == nil || err.Error() != "not now" {
		t.Errorf("a refusal by the opener: %v", err)
	}
}
