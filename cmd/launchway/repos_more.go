package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// More of launchway repos: snapshots of a ref, and the hosting settings. (The protected tags are in keys.go.)

const reposMoreUsage = `  launchway repos download OWNER/NAME [--ref REF] [--format tar.gz|zip] [-o FILE]
                                                         save a snapshot of a branch, tag or commit (read access; the server limits its size)
  launchway repos settings [--archive-max-mib N] [--trash-days N] [--json]
                                                         the largest snapshot and how long a deleted repository is kept (changing them: administrators)
`

func (cl *client) reposDownload(args []string) error {
	fs := flag.NewFlagSet("repos download", flag.ContinueOnError)
	ref := fs.String("ref", "", "the branch, tag or commit (default: the default branch)")
	format := fs.String("format", "tar.gz", "tar.gz or zip")
	out := fs.String("o", "", "the file to write (default: NAME-REF.FORMAT, in this directory)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway repos download OWNER/NAME [--ref REF] [--format tar.gz|zip] [-o FILE]")
	}
	path, err := repoPath(pos[0])
	if err != nil {
		return err
	}
	q := url.Values{"format": {*format}}
	if *ref != "" {
		q.Set("ref", *ref)
	}
	dest := *out
	if dest == "" {
		_, name, _ := splitRepo(pos[0])
		dest = name + "-" + strings.NewReplacer("/", "-", " ", "-").Replace(nonEmptyStr(*ref, "snapshot")) + "." + *format
	}
	return cl.download(path+"/download?"+q.Encode(), dest)
}

func (cl *client) reposSettings(args []string) error {
	fs := flag.NewFlagSet("repos settings", flag.ContinueOnError)
	archive := fs.Int("archive-max-mib", 0, "the most a snapshot may hold, in MiB of files")
	trash := fs.Int("trash-days", 0, "how many days a deleted repository can be restored")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var s struct {
		ArchiveMaxMiB      int `json:"archive_max_mib"`
		TrashRetentionDays int `json:"trash_retention_days"`
	}
	var err error
	if v := visited(fs); v["archive-max-mib"] || v["trash-days"] {
		body := map[string]any{}
		if v["archive-max-mib"] {
			body["archive_max_mib"] = *archive
		}
		if v["trash-days"] {
			body["trash_retention_days"] = *trash
		}
		err = cl.call("PUT", "/api/v1/hosting-settings", body, &s)
	} else {
		err = cl.call("GET", "/api/v1/hosting-settings", nil, &s)
	}
	if err != nil {
		return err
	}
	return cl.print(s, *asJSON, func() {
		fmt.Fprintf(cl.out, "A snapshot of a ref may hold up to %d MiB of files.\nA deleted repository is kept %d days before it is removed for good.\n", s.ArchiveMaxMiB, s.TrashRetentionDays)
	})
}
