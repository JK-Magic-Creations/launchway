package gitssh

import (
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	ok := map[string]Command{
		"git-upload-pack 'acme/shop.git'":       {Service: UploadPack, Path: "acme/shop"},
		"git-upload-pack '/acme/shop.git'":      {Service: UploadPack, Path: "acme/shop"},
		`git-receive-pack "acme/shop"`:          {Service: ReceivePack, Path: "acme/shop"},
		"git-upload-archive 'acme/shop.git'":    {Service: UploadArchive, Path: "acme/shop"},
		"git-upload-pack acme/my-repo_1.v2.git": {Service: UploadPack, Path: "acme/my-repo_1.v2"},
	}
	for line, want := range ok {
		got, err := ParseCommand(line)
		if err != nil || got != want {
			t.Errorf("%q: %+v, %v; want %+v", line, got, err, want)
		}
	}
	for line, why := range map[string]string{
		"ls /":                               "shell access is not provided",
		"bash -c 'id'":                       "shell access is not provided",
		"git-upload-pack":                    "owner/name",
		"git-upload-pack '../../etc/passwd'": "owner/name",
		"git-upload-pack 'a/b/c'":            "owner/name",
		"git-upload-pack 'acme/shop; id'":    "owner/name",
		"git-upload-pack 'acme/shop' extra":  "owner/name",
		"scp -t /tmp":                        "shell access is not provided",
		"":                                   "shell access is not provided",
	} {
		if _, err := ParseCommand(line); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: %v, want a refusal mentioning %q", line, err, why)
		}
	}
	// One line, plain words.
	_, err := ParseCommand("ls")
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Errorf("the refusal is not one line: %v", err)
	}
}
