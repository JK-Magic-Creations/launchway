package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verify exits 1 and names what is missing until everything is imported, then proves equality.
func TestCLIReposVerify(t *testing.T) {
	hc := newHostedCLI(t)
	root := t.TempDir()
	for _, n := range []string{"shop", "blog"} {
		bare, _ := bareWith(t)
		if err := os.MkdirAll(filepath.Join(root, "old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(bare, filepath.Join(root, "old", n+".git")); err != nil {
			t.Fatal(err)
		}
	}
	other := otherServer(t, root)
	var ao bytes.Buffer
	ada := hc.client("ada", "root", &ao)
	urls := []string{"--url", other.URL + "/old/shop.git", "--url", other.URL + "/old/blog.git"}
	ao.Reset()
	if err := ada.reposVerify(urls); err == nil || !strings.Contains(ao.String(), "NOT equal") || !strings.Contains(ao.String(), "missing") {
		t.Fatalf("verify before import: %v %q", err, ao.String())
	} else if ce, ok := err.(cliErr); !ok || ce.code != 1 {
		t.Fatalf("exit code: %v", err)
	}
	ao.Reset()
	if err := ada.reposImport(urls); err != nil {
		t.Fatal(err)
	}
	ao.Reset()
	if err := ada.reposVerify(urls); err != nil || !strings.Contains(ao.String(), "Every ref of all 2 repositories equals the source's") {
		t.Fatalf("verify: %v %q", err, ao.String())
	}
	if err := hc.client("otto", "ops", &bytes.Buffer{}).reposVerify(urls); err == nil {
		t.Fatal("an operator verified")
	}
	_ = strings.Contains
}

// import-all brings in everything a connection lists, as ordinary repositories unless --mirror, checks each ref by ref,
// and is safe to run again.
func TestCLIReposImportAll(t *testing.T) {
	hc := newHostedCLI(t)
	root := t.TempDir()
	for _, n := range []string{"shop", "blog"} {
		bare, _ := bareWith(t)
		if err := os.MkdirAll(filepath.Join(root, "old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(bare, filepath.Join(root, "old", n+".git")); err != nil {
			t.Fatal(err)
		}
	}
	other := otherServer(t, root)
	var ao bytes.Buffer
	ada := hc.client("ada", "root", &ao)
	if err := ada.reposConnection([]string{"add", "oldgit", "--kind", "plain"}); err != nil {
		t.Fatal(err)
	}
	urls := []string{"--connection", "oldgit", "--url", other.URL + "/old/shop.git", "--url", other.URL + "/old/blog.git"}
	ao.Reset()
	if err := ada.reposImportAll(append([]string{"--dry-run"}, urls...)); err != nil || !strings.Contains(ao.String(), "Nothing was changed") {
		t.Fatalf("dry run: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.reposImportAll(urls); err != nil || !strings.Contains(ao.String(), "2 verified") {
		t.Fatalf("import-all: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.reposImportAll(urls); err != nil {
		t.Fatalf("again: %v %q", err, ao.String())
	}
	if err := ada.reposImportAll([]string{}); err == nil {
		t.Fatal("a connection is required")
	}
	if err := hc.client("otto", "ops", &bytes.Buffer{}).reposImportAll(urls); err == nil {
		t.Fatal("an operator imported everything")
	}
}

// --against takes an address as well as a connection's name; --refs lists every ref with its hashes.
func TestCLIReposVerifyAgainstAnAddressWithRefs(t *testing.T) {
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
	var ao bytes.Buffer
	ada := hc.client("ada", "root", &ao)
	addr := other.URL + "/old/shop.git"
	if err := ada.reposImport([]string{"--url", addr}); err != nil {
		t.Fatal(err)
	}
	ao.Reset()
	if err := ada.reposVerify([]string{"--against", addr, "--refs"}); err != nil {
		t.Fatalf("%v %q", err, ao.String())
	}
	out := ao.String()
	for _, want := range []string{"NAMESPACE", "refs/heads", "STATUS", "equal", "refs/heads/main", "Every ref of all 1 repositories"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}
