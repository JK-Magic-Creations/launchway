package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cycle stages paths of one workspace, packs them, unpacks the archive and
// places the files in another workspace, as a save and a later restore do.
func cycle(t *testing.T, from, to string, globs []string) {
	t.Helper()
	ctx := context.Background()
	staged, err := Stage(ctx, nil, Spec{Workspace: from}, globs, StageLimits{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := PackCache(staged.Files, filepath.Join(t.TempDir(), "archive"))
	if err != nil {
		t.Fatal(err)
	}
	un, err := UnpackCache(ctx, a.Path, t.TempDir(), globs, UnpackLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Place(ctx, nil, Spec{Workspace: to}, un.Files, nil); err != nil {
		t.Fatal(err)
	}
}

func mtimeOf(t *testing.T, p string) time.Time {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime()
}

func writeAged(t *testing.T, p string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

// A restored file keeps the time it was saved with. Without that, every
// restored file is stamped with the moment of the restore, newer than every
// source of a fresh checkout, and a tool that compares times keeps output
// built from other sources.
func TestRestoredFilesKeepTheirModificationTimes(t *testing.T) {
	saved, fresh := t.TempDir(), t.TempDir()
	// The run that saved: a source, then the object a build made from it.
	writeAged(t, filepath.Join(saved, "target", "a.o"), 48*time.Hour)
	writeAged(t, filepath.Join(saved, ".m2", "repo", "x", "maven-metadata.xml.lastUpdated"), 30*time.Hour)
	// A later run: the checkout is made now.
	writeAged(t, filepath.Join(fresh, "a.c"), 0)

	cycle(t, saved, fresh, []string{"target", ".m2"})

	// Make-style: a.o is up to date only if it is not older than a.c.
	src, obj := mtimeOf(t, filepath.Join(fresh, "a.c")), mtimeOf(t, filepath.Join(fresh, "target", "a.o"))
	if !obj.Before(src) {
		t.Fatalf("make would see a.o (%v) as newer than the checkout's a.c (%v) and keep an object built from other sources", obj, src)
	}
	// Maven-style: a resolver re-checks a "last updated" marker that is a
	// day old or more; a marker stamped with the restore would look fresh.
	marker := mtimeOf(t, filepath.Join(fresh, ".m2", "repo", "x", "maven-metadata.xml.lastUpdated"))
	if age := time.Since(marker); age < 29*time.Hour {
		t.Fatalf("the marker looks %v old after the restore; it was 30h old when saved", age)
	}
}

// An archive cannot date a file after the restore: it would look newer than
// the sources of the checkout.
func TestRestoredTimeIsNeverInTheFuture(t *testing.T) {
	saved, fresh := t.TempDir(), t.TempDir()
	writeAged(t, filepath.Join(saved, "out", "f"), -240*time.Hour)
	cycle(t, saved, fresh, []string{"out"})
	if got := mtimeOf(t, filepath.Join(fresh, "out", "f")); got.After(time.Now().Add(time.Second)) {
		t.Fatalf("restored file is dated %v, in the future", got)
	}
}
