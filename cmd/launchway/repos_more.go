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
  launchway repos settings [--archive-max-mib N] [--trash-days N] [--stale-days N] [--json]
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
	stale := fs.Int("stale-days", 0, "how many days without a commit make a branch stale")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var s struct {
		ArchiveMaxMiB      int `json:"archive_max_mib"`
		TrashRetentionDays int `json:"trash_retention_days"`
		StaleBranchDays    int `json:"stale_branch_days"`
	}
	var err error
	if v := visited(fs); v["archive-max-mib"] || v["trash-days"] || v["stale-days"] {
		body := map[string]any{}
		if v["archive-max-mib"] {
			body["archive_max_mib"] = *archive
		}
		if v["trash-days"] {
			body["trash_retention_days"] = *trash
		}
		if v["stale-days"] {
			body["stale_branch_days"] = *stale
		}
		err = cl.call("PUT", "/api/v1/hosting-settings", body, &s)
	} else {
		err = cl.call("GET", "/api/v1/hosting-settings", nil, &s)
	}
	if err != nil {
		return err
	}
	return cl.print(s, *asJSON, func() {
		fmt.Fprintf(cl.out, "A snapshot of a ref may hold up to %d MiB of files.\nA deleted repository is kept %d days before it is removed for good.\nA branch is stale after %d days without a commit.\n", s.ArchiveMaxMiB, s.TrashRetentionDays, s.StaleBranchDays)
	})
}

// releaseBadges shows, or switches on or off, a Flight's badges.
func (cl *client) releaseBadges(args []string) error {
	fs := flag.NewFlagSet("release badges", flag.ContinueOnError)
	on := fs.Bool("on", false, "show the badges to anyone (an administrator of the Flight)")
	off := fs.Bool("off", false, "hide them again")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*on && *off) {
		return usageErr("launchway release badges FLIGHT [--on|--off] [--json]")
	}
	path := "/api/v1/projects/" + url.PathEscape(pos[0]) + "/badges"
	var v struct {
		Enabled          bool              `json:"enabled"`
		PublicRepository bool              `json:"public_repository"`
		Shown            bool              `json:"shown"`
		Markdown         map[string]string `json:"markdown"`
	}
	if *on || *off {
		err = cl.call("PUT", path, map[string]any{"enabled": *on}, &v)
	} else {
		err = cl.call("GET", path, nil, &v)
	}
	if err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		switch {
		case v.PublicRepository:
			fmt.Fprintln(cl.out, "Badges are shown to anyone: the Flight's repository is public.")
		case v.Enabled:
			fmt.Fprintln(cl.out, "Badges are shown to anyone: switched on for this Flight.")
		default:
			fmt.Fprintln(cl.out, "Badges are off: they answer not found until an administrator of the Flight turns them on (launchway release badges FLIGHT --on).")
		}
		fmt.Fprintln(cl.out, "Status of the last Sortie on a branch: "+v.Markdown["status"])
		fmt.Fprintln(cl.out, "Latest release:                        "+v.Markdown["release"])
	})
}
