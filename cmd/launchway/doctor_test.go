package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := f()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), err
}

// With one database the doctor says plainly that the installation is not
// highly available, what a failure stops, and what is known about backups.
func TestDoctorDatabaseSaysASingleDatabaseIsNotHighlyAvailable(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "launchway.db")
	eng, err := engine.Open(engine.Options{Database: db})
	if err != nil {
		t.Fatal(err)
	}
	eng.Close()
	cfg := filepath.Join(dir, "launchway.yaml")
	if err := os.WriteFile(cfg, []byte("data: "+dir+"\ndatabase: "+db+"\nresilience:\n  stall_budget: 90s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAUNCHWAY_DATABASE", "")
	out, err := captureStdout(t, func() error { return cmdDoctor([]string{"database", "--config", cfg}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"not highly available", "stops Launchway until the file is restored", "last backup", "1m30s"} {
		if !strings.Contains(out, want) {
			t.Errorf("the doctor's answer lacks %q:\n%s", want, out)
		}
	}
}
