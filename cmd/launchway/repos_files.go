package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"text/tabwriter"
)

// reposFilesUsage is the part of the repos help about a repository's contents.
const reposFilesUsage = `
  launchway repos commits OWNER/NAME [--ref R] [--path P] [--limit N] [--json]
                                    the newest commits of a branch, tag or commit, optionally only those that
                                    changed a path
  launchway repos files OWNER/NAME [PATH] [--ref R] [--json]
                                    a directory's entries, or a file's text on standard output
  launchway repos put-file OWNER/NAME PATH [--file F | --text TEXT] --message M [--branch B] [--expect-sha SHA] [--json]
                                    one commit that adds a file (or, with --expect-sha, replaces it); the
                                    branch must exist (operators; protected branches: administrators)
  launchway repos upload OWNER/NAME FILE... --message M [--dir D] [--branch B] [--replace] [--json]
                                    one commit that adds several local files (at most 100, 1 MiB each, 8 MiB in all) under
                                    the folder D; an existing file is a conflict unless --replace (operators; protected
                                    branches: administrators)
  launchway repos delete-file OWNER/NAME PATH --message M --expect-sha SHA [--branch B] [--json]
                                    one commit that removes a file; --expect-sha is its current id
`

func (cl *client) reposCommits(args []string) error {
	fs := flag.NewFlagSet("repos commits", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	path := fs.String("path", "", "only commits that changed this path")
	limit := fs.Int("limit", 0, "how many, at most 200 (default 30)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos commits OWNER/NAME [--ref R] [--path P] [--limit N]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range map[string]string{"ref": *ref, "path": *path} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	var out struct {
		Ref   string `json:"ref"`
		Items []struct {
			SHA      string `json:"sha"`
			Subject  string `json:"subject"`
			Author   string `json:"author"`
			AuthorAt string `json:"authored_at"`
		} `json:"items"`
	}
	if err := cl.call("GET", base+"/commits?"+q.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 0, 4, 2, ' ', 0)
		for _, c := range out.Items {
			fmt.Fprintf(tw, "%.10s\t%.10s\t%s\t%s\n", c.SHA, c.AuthorAt, c.Author, c.Subject)
		}
		_ = tw.Flush()
	})
}

func (cl *client) reposFiles(args []string) error {
	fs := flag.NewFlagSet("repos files", flag.ContinueOnError)
	ref := fs.String("ref", "", "a branch, tag or commit (default: the default branch)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageErr("launchway repos files OWNER/NAME [PATH] [--ref R]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{}
	if len(pos) == 2 {
		q.Set("path", pos[1])
	}
	if *ref != "" {
		q.Set("ref", *ref)
	}
	var it struct {
		Path      string `json:"path"`
		Type      string `json:"type"`
		SHA       string `json:"sha"`
		Size      int64  `json:"size"`
		Encoding  string `json:"encoding"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err := cl.call("GET", base+"/files?"+q.Encode(), nil, &it); err != nil {
		return err
	}
	return cl.print(it, *asJSON, func() {
		switch {
		case it.Type == "dir":
			for _, e := range it.Entries {
				name := e.Name
				if e.Type == "dir" {
					name += "/"
				}
				fmt.Fprintf(cl.out, "%-10s %10d  %s\n", e.Type, e.Size, name)
			}
		case it.Truncated:
			fmt.Fprintf(cl.out, "%s is %d bytes, too large to read through the API; clone the repository.\n", it.Path, it.Size)
		case it.Encoding == "base64":
			b, _ := base64.StdEncoding.DecodeString(it.Content)
			_, _ = cl.out.Write(b)
		default:
			fmt.Fprint(cl.out, it.Content)
		}
	})
}

func (cl *client) reposPutFile(args []string) error {
	fs := flag.NewFlagSet("repos put-file", flag.ContinueOnError)
	file := fs.String("file", "", "read the content from this file (- is standard input)")
	text := fs.String("text", "", "the content as text")
	msg := fs.String("message", "", "the commit message")
	branch := fs.String("branch", "", "the branch (default: the default branch); it must exist")
	expect := fs.String("expect-sha", "", "to replace a file: its current id (repos files --json shows it)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *msg == "" || (*file == "") == (*text == "") {
		return usageErr("launchway repos put-file OWNER/NAME PATH (--file F | --text TEXT) --message M [--branch B] [--expect-sha SHA]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	content := []byte(*text)
	if *file != "" {
		if *file == "-" {
			content, err = io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
		} else {
			content, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
	}
	body := map[string]any{"path": pos[1], "message": *msg, "branch": *branch, "expected_sha": *expect, "encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)}
	var out struct {
		Branch string `json:"branch"`
		Path   string `json:"path"`
		Commit string `json:"commit"`
		SHA    string `json:"sha"`
	}
	if err := cl.call("PUT", base+"/files", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Committed %s to %s as %.10s.\n", out.Path, out.Branch, out.Commit) })
}

func (cl *client) reposDeleteFile(args []string) error {
	fs := flag.NewFlagSet("repos delete-file", flag.ContinueOnError)
	msg := fs.String("message", "", "the commit message")
	branch := fs.String("branch", "", "the branch (default: the default branch); it must exist")
	expect := fs.String("expect-sha", "", "the file's current id (repos files --json shows it)")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *msg == "" || *expect == "" {
		return usageErr("launchway repos delete-file OWNER/NAME PATH --message M --expect-sha SHA [--branch B]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	body := map[string]any{"path": pos[1], "message": *msg, "branch": *branch, "expected_sha": *expect}
	var out struct {
		Branch string `json:"branch"`
		Path   string `json:"path"`
		Commit string `json:"commit"`
	}
	if err := cl.call("DELETE", base+"/files", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() { fmt.Fprintf(cl.out, "Removed %s from %s in %.10s.\n", out.Path, out.Branch, out.Commit) })
}

func (cl *client) reposUpload(args []string) error {
	fs := flag.NewFlagSet("repos upload", flag.ContinueOnError)
	msg := fs.String("message", "", "the commit message")
	branch := fs.String("branch", "", "the branch (default: the default branch); it must exist")
	dir := fs.String("dir", "", "the folder in the repository to put the files in")
	replace := fs.Bool("replace", false, "overwrite files that exist")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 || *msg == "" {
		return usageErr("launchway repos upload OWNER/NAME FILE... --message M [--dir D] [--branch B] [--replace]")
	}
	base, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	type file struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	var fl []file
	for _, f := range pos[1:] {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		fl = append(fl, file{Path: path.Join(*dir, filepath.Base(f)), Content: base64.StdEncoding.EncodeToString(b), Encoding: "base64"})
	}
	var out struct {
		Branch string   `json:"branch"`
		Paths  []string `json:"paths"`
		Commit string   `json:"commit"`
	}
	if err := cl.call("POST", base+"/files", map[string]any{"branch": *branch, "message": *msg, "replace": *replace, "files": fl}, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Committed %d file(s) to %s as %.10s.\n", len(out.Paths), out.Branch, out.Commit)
	})
}
