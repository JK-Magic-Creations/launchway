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
