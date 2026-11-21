package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commits, files and put-file against a repository brought in with --once (an ordinary repository),
// and the refusal on one that is still a mirror.
func TestCLIReposCommitsFilesPutFile(t *testing.T) {
	hc := newHostedCLI(t)
	root := t.TempDir()
	bare, _ := bareWith(t)
	if err := os.MkdirAll(filepath.Join(root, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bare, filepath.Join(root, "old", "shop.git")); err != nil {
		t.Fatal(err)
	}
	other := otherServer(t, root)
	var out bytes.Buffer
	ada := hc.client("ada", "root", &out)
	addr := other.URL + "/old/shop.git"
	if err := ada.reposImport([]string{"--url", addr}); err != nil { // a mirror: takes no changes
		t.Fatal(err)
	}
	out.Reset()
	if err := ada.reposPutFile([]string{"old/shop", "x.txt", "--text", "hi", "--message", "m"}); err == nil || !strings.Contains(err.Error(), "mirror") {
		t.Fatalf("a mirror took a file: %v", err)
	}
	if err := ada.reposMirror([]string{"stop", "old/shop"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ada.reposPutFile([]string{"old/shop", "docs/x.txt", "--text", "hello\n", "--message", "add x"}); err != nil || !strings.Contains(out.String(), "Committed docs/x.txt to main") {
		t.Fatalf("put-file: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposFiles([]string{"old/shop", "docs/x.txt"}); err != nil || out.String() != "hello\n" {
		t.Fatalf("files: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposFiles([]string{"old/shop"}); err != nil || !strings.Contains(out.String(), "docs/") || !strings.Contains(out.String(), "a") {
		t.Fatalf("ls: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposCommits([]string{"old/shop", "--limit", "5"}); err != nil || !strings.Contains(out.String(), "add x") || !strings.Contains(out.String(), "one") {
		t.Fatalf("commits: %v %q", err, out.String())
	}
	if err := ada.reposPutFile([]string{"old/shop", "docs/x.txt", "--text", "again", "--message", "m"}); err == nil || !strings.Contains(err.Error(), "expected_sha") {
		t.Fatalf("a blind overwrite: %v", err)
	}
}
