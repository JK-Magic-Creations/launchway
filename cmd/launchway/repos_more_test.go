package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The command line reaches the protected tags, a snapshot of a ref and the hosting settings through the same
// routes as the API: tags are set and shown, a tar.gz is saved (and not over a file that is there), and the
// settings are read, changed and refused when out of bounds.
func TestCLIProtectedTagsSnapshotAndSettings(t *testing.T) {
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
	if err := ada.reposImport([]string{"--url", other.URL + "/old/shop.git", "--once"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()

	if err := ada.protectedTags([]string{"old/shop"}); err != nil || !strings.Contains(out.String(), "No protected tags") {
		t.Fatalf("none yet: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.protectedTags([]string{"old/shop", "--rule", "v*:create=admin,change=none,pipelines=ship-*|web", "--rule", "rc-*:create=operator,change=admin"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v*: the admin role may make one; nobody may move or delete one; Flights admitted to make one: ship-*, web", "rc-*: the operator role may make one; the admin role may move or delete one"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := ada.protectedTags([]string{"old/shop", "--rule", "v[*"}); err == nil || !strings.Contains(err.Error(), "not a tag name or pattern") {
		t.Fatalf("a bad pattern: %v", err)
	}
	if err := ada.protectedTags([]string{"old/shop", "--clear"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ada.protectedTags([]string{"old/shop"}); err != nil || !strings.Contains(out.String(), "No protected tags") {
		t.Fatalf("cleared: %v %q", err, out.String())
	}

	dest := filepath.Join(t.TempDir(), "shop.tar.gz")
	out.Reset()
	if err := ada.reposDownload([]string{"old/shop", "-o", dest}); err != nil || !strings.Contains(out.String(), "wrote "+dest) {
		t.Fatalf("download: %v %q", err, out.String())
	}
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr, n := tar.NewReader(gz), 0
	for {
		if _, err := tr.Next(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("the snapshot is empty")
	}
	if err := ada.reposDownload([]string{"old/shop", "-o", dest}); err == nil {
		t.Fatal("a snapshot was written over a file that is there")
	}

	out.Reset()
	if err := ada.reposSettings(nil); err != nil || !strings.Contains(out.String(), "512 MiB") || !strings.Contains(out.String(), "30 days") {
		t.Fatalf("settings: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposSettings([]string{"--archive-max-mib", "64", "--trash-days", "7"}); err != nil || !strings.Contains(out.String(), "64 MiB") || !strings.Contains(out.String(), "7 days") {
		t.Fatalf("set: %v %q", err, out.String())
	}
	if err := ada.reposSettings([]string{"--trash-days", "0"}); err == nil {
		t.Fatal("0 days was accepted")
	}
}

// The badges of a Flight: off by default and said so, with the Markdown to paste; an operator who is not an
// administrator of the Flight cannot switch them on, and the refusal says why.
func TestCLIReleaseBadges(t *testing.T) {
	c, _ := cliServer(t)
	var out bytes.Buffer
	cl := newClient(c, &out)
	if err := cl.releaseBadges([]string{"app"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Badges are off", "![app status](", "/public/badges/app/status.svg?branch=main", "/public/badges/app/release.svg"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if err := cl.releaseBadges([]string{"app", "--on"}); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Errorf("an operator switched badges on: %v", err)
	}
	if err := cl.releaseBadges([]string{"app", "--on", "--off"}); err == nil {
		t.Error("on and off together")
	}
}
