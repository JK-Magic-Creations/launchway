package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const registryUsage = `Usage:
  launchway registry status [--json]                       the built-in registry: address, login, limits, what protects the blobs
  launchway registry list PROJECT [--json]                 a project's repositories, with usage against its quota
  launchway registry list REPOSITORY [--json]              a repository's images: tags, digest, size, platforms, who pushed it, how to pull it
  launchway registry tags REPOSITORY                       a repository's tags and the digest each points at
  launchway registry inspect REPOSITORY REFERENCE          one image or artifact by tag or digest, with its manifest
  launchway registry delete REPOSITORY REFERENCE [--tag]   delete an image (with its signatures and SBOMs), or with --tag only the tag
  launchway registry retention PROJECT|REPOSITORY [--run]  what the retention rules would delete; --run deletes it
  launchway registry policy get [PROJECT|REPOSITORY]       quota, retention, immutable tags (and the effective values)
  launchway registry policy set [PROJECT|REPOSITORY]       change them
                     [--quota-gib N] [--keep-last N] [--keep-tags P,P] [--untagged-days N] [--immutable-tags P,P] [--pull-from P,P]
  launchway registry gc [--dry-run] [--last]               collect garbage in the blob store (administrators)

A repository is PROJECT/NAME (the project first): pushing to <registry host>/PROJECT/NAME makes it. Reading needs
the viewer role on the project, deleting and policy the admin role; collection is an administrator's. Pipelines
push and pull with a credential made for each run; people use an API token with docker, podman, skopeo or helm.
Deleting, policy changes, retention and collection are recorded in the audit trail. Blobs are not encrypted by
Launchway in this release: see "registry status".
`

func init() { commands["registry"] = cmdRegistry }

func cmdRegistry(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, registryUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "status":
			return cl.registryStatus(args[1:])
		case "list":
			return cl.registryList(args[1:])
		case "tags":
			return cl.registryTags(args[1:])
		case "inspect":
			return cl.registryInspect(args[1:])
		case "delete":
			return cl.registryDelete(args[1:])
		case "retention":
			return cl.registryRetention(args[1:])
		case "policy":
			return cl.registryPolicy(args[1:])
		case "gc":
			return cl.registryGC(args[1:])
		}
		return usageErr("unknown command registry %s\n\n%s", args[0], registryUsage)
	})
}

func projectOfRepo(repo string) string {
	p, _, _ := strings.Cut(repo, "/")
	return p
}

func (cl *client) registryStatus(args []string) error {
	fs := flag.NewFlagSet("registry status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var v struct {
		Address      string `json:"address"`
		Host         string `json:"host"`
		Insecure     bool   `json:"insecure"`
		Store        string `json:"store"`
		Login        string `json:"login"`
		Repositories int    `json:"repositories"`
		Encryption   struct {
			Encrypted bool   `json:"blobs_encrypted_by_launchway"`
			Note      string `json:"note"`
		} `json:"encryption"`
		Limits struct {
			MaxBlob   int64 `json:"max_blob_bytes"`
			Quota     int64 `json:"default_quota_bytes"`
			PerMinute int   `json:"requests_per_minute"`
		} `json:"limits"`
	}
	if err := cl.call("GET", "/api/v1/registry", nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		fmt.Fprintf(cl.out, "Built-in registry at %s (blob store: %s); you can see %d repositories.\n", v.Address, v.Store, v.Repositories)
		fmt.Fprintf(cl.out, "Log in:  %s\n", v.Login)
		if v.Insecure {
			fmt.Fprintln(cl.out, "It is plain HTTP: clients need --tls-verify=false (podman, skopeo) or --insecure (docker, helm --plain-http).")
		}
		fmt.Fprintf(cl.out, "Limits:  a blob up to %s, a project %s by default, %d requests a minute per caller\n", byteText(v.Limits.MaxBlob), byteText(v.Limits.Quota), v.Limits.PerMinute)
		fmt.Fprintf(cl.out, "At rest: %s\n", v.Encryption.Note)
	})
}

func (cl *client) registryList(args []string) error {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway registry list PROJECT|REPOSITORY [--json]")
	}
	if !strings.Contains(pos[0], "/") {
		var v struct {
			Repositories []struct {
				Name   string `json:"name"`
				Images int    `json:"images"`
				Tags   int    `json:"tags"`
				Bytes  int64  `json:"bytes"`
			} `json:"repositories"`
			Used  int64  `json:"used_bytes"`
			Quota int64  `json:"quota_bytes"`
			Host  string `json:"host"`
		}
		if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/registry", nil, &v); err != nil {
			return err
		}
		return cl.print(v, *asJSON, func() {
			if len(v.Repositories) == 0 {
				fmt.Fprintf(cl.out, "Nothing is pushed yet: docker push %s/%s/<image>:<tag>\n", v.Host, pos[0])
			}
			for _, r := range v.Repositories {
				fmt.Fprintf(cl.out, "%-40s %4d images %4d tags %10s\n", r.Name, r.Images, r.Tags, byteText(r.Bytes))
			}
			quota := "no quota"
			if v.Quota >= 0 {
				quota = "of " + byteText(v.Quota)
			}
			fmt.Fprintf(cl.out, "%s stored %s\n", byteText(v.Used), quota)
		})
	}
	var v struct {
		Images []imageLine `json:"images"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(projectOfRepo(pos[0]))+"/registry/images?repository="+url.QueryEscape(pos[0]), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		if len(v.Images) == 0 {
			fmt.Fprintln(cl.out, "No images in this repository.")
		}
		for _, im := range v.Images {
			printImage(cl, im)
		}
	})
}

type imageLine struct {
	Digest          string            `json:"digest"`
	MediaType       string            `json:"media_type"`
	ArtifactType    string            `json:"artifact_type"`
	Tags            []string          `json:"tags"`
	TotalSize       int64             `json:"total_size"`
	Platforms       []string          `json:"platforms"`
	CreatedAt       int64             `json:"created_at"`
	CreatedBy       string            `json:"created_by"`
	RunID           string            `json:"run_id"`
	Signed          bool              `json:"signed"`
	HasSBOM         bool              `json:"has_sbom"`
	Pull            string            `json:"pull"`
	Annotations     map[string]string `json:"annotations"`
	Vulnerabilities *struct {
		Critical int `json:"critical"`
		High     int `json:"high"`
		Medium   int `json:"medium"`
		Low      int `json:"low"`
	} `json:"vulnerabilities"`
}

func printImage(cl *client, im imageLine) {
	tags := strings.Join(im.Tags, ", ")
	if tags == "" {
		tags = "(untagged)"
	}
	d := im.Digest
	if len(d) > 19 {
		d = d[:19]
	}
	kind := ""
	if im.ArtifactType != "" {
		kind = " [" + im.ArtifactType + "]"
	}
	var marks []string
	if im.Signed {
		marks = append(marks, "signed")
	}
	if im.HasSBOM {
		marks = append(marks, "sbom")
	}
	if v := im.Vulnerabilities; v != nil {
		marks = append(marks, fmt.Sprintf("vulnerabilities %dC %dH %dM %dL", v.Critical, v.High, v.Medium, v.Low))
	}
	by := im.CreatedBy
	if im.RunID != "" {
		by += " (run " + im.RunID + ")"
	}
	fmt.Fprintf(cl.out, "%-28s %s %10s  %s  by %s%s\n", tags, d, byteText(im.TotalSize), time.UnixMilli(im.CreatedAt).UTC().Format("2006-01-02 15:04"), by, kind)
	if len(im.Platforms) > 0 || len(marks) > 0 {
		fmt.Fprintf(cl.out, "    %s %s\n", strings.Join(im.Platforms, ","), strings.Join(marks, ", "))
	}
	fmt.Fprintf(cl.out, "    pull: %s\n", im.Pull)
}

func (cl *client) registryTags(args []string) error {
	fs := flag.NewFlagSet("registry tags", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !strings.Contains(pos[0], "/") {
		return usageErr("launchway registry tags REPOSITORY [--json]")
	}
	var v struct {
		Images []imageLine `json:"images"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(projectOfRepo(pos[0]))+"/registry/images?repository="+url.QueryEscape(pos[0]), nil, &v); err != nil {
		return err
	}
	tags := map[string]string{}
	for _, im := range v.Images {
		for _, t := range im.Tags {
			tags[t] = im.Digest
		}
	}
	return cl.print(tags, *asJSON, func() {
		for t, d := range tags {
			fmt.Fprintf(cl.out, "%-30s %s\n", t, d)
		}
	})
}

func (cl *client) registryInspect(args []string) error {
	fs := flag.NewFlagSet("registry inspect", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway registry inspect REPOSITORY REFERENCE [--json]")
	}
	var v struct {
		Image    imageLine `json:"image"`
		Manifest string    `json:"manifest"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(projectOfRepo(pos[0]))+"/registry/image?repository="+url.QueryEscape(pos[0])+"&reference="+url.QueryEscape(pos[1]), nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		printImage(cl, v.Image)
		fmt.Fprintf(cl.out, "digest: %s\nmanifest:\n%s\n", v.Image.Digest, indent(v.Manifest))
	})
}

func indent(manifest string) string {
	var v any
	if json.Unmarshal([]byte(manifest), &v) != nil {
		return manifest
	}
	b, _ := json.MarshalIndent(v, "  ", "  ")
	return "  " + string(b)
}

func (cl *client) registryDelete(args []string) error {
	fs := flag.NewFlagSet("registry delete", flag.ContinueOnError)
	tag := fs.Bool("tag", false, "delete only this tag; the image stays")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway registry delete REPOSITORY REFERENCE [--tag]")
	}
	base := "/api/v1/projects/" + url.PathEscape(projectOfRepo(pos[0])) + "/registry/"
	path := base + "image?repository=" + url.QueryEscape(pos[0]) + "&reference=" + url.QueryEscape(pos[1])
	if *tag {
		path = base + "tag?repository=" + url.QueryEscape(pos[0]) + "&tag=" + url.QueryEscape(pos[1])
	}
	var v map[string]any
	if err := cl.call("DELETE", path, nil, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		if *tag {
			fmt.Fprintf(cl.out, "Deleted the tag %s of %s. The image stays.\n", pos[1], pos[0])
			return
		}
		fmt.Fprintf(cl.out, "Deleted %s of %s with what was attached to it. The bytes are removed by the next garbage collection.\n", pos[1], pos[0])
	})
}

type candidateLine struct {
	Repo      string   `json:"repository"`
	Digest    string   `json:"digest"`
	Tags      []string `json:"tags"`
	Reason    string   `json:"reason"`
	Size      int64    `json:"size"`
	CreatedAt int64    `json:"created_at"`
}

func (cl *client) registryRetention(args []string) error {
	fs := flag.NewFlagSet("registry retention", flag.ContinueOnError)
	run := fs.Bool("run", false, "delete what the preview lists")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway registry retention PROJECT|REPOSITORY [--run]")
	}
	path := "/api/v1/projects/" + url.PathEscape(projectOfRepo(pos[0])) + "/registry/retention"
	if strings.Contains(pos[0], "/") {
		path += "?repository=" + url.QueryEscape(pos[0])
	}
	method := "GET"
	if *run {
		method = "POST"
	}
	var v struct {
		DryRun     bool            `json:"dry_run"`
		Candidates []candidateLine `json:"candidates"`
		Deleted    []candidateLine `json:"deleted"`
	}
	var body any
	if *run {
		body = map[string]any{}
	}
	if err := cl.call(method, path, body, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		list, verb := v.Candidates, "Would delete"
		if *run {
			list, verb = v.Deleted, "Deleted"
		}
		for _, c := range list {
			fmt.Fprintf(cl.out, "%s %s@%s %v (%s): %s\n", verb, c.Repo, c.Digest[:19], c.Tags, byteText(c.Size), c.Reason)
		}
		if len(list) == 0 {
			fmt.Fprintln(cl.out, "Nothing to delete: the retention rules keep everything (or no rule is set).")
		} else if !*run {
			fmt.Fprintf(cl.out, "%d images. Nothing was deleted; run again with --run to delete them.\n", len(list))
		}
	})
}

func registryPolicyPath(target string) string {
	if target == "" {
		return "/api/v1/registry/policy"
	}
	p := "/api/v1/projects/" + url.PathEscape(projectOfRepo(target)) + "/registry/policy"
	if strings.Contains(target, "/") {
		p += "?repository=" + url.QueryEscape(target)
	}
	return p
}

func (cl *client) registryPolicy(args []string) error {
	if len(args) == 0 {
		return usageErr("%s", registryUsage)
	}
	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("registry policy get", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print as JSON")
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		target := ""
		if len(pos) == 1 {
			target = pos[0]
		}
		var v map[string]any
		if err := cl.call("GET", registryPolicyPath(target), nil, &v); err != nil {
			return err
		}
		return cl.print(v, *asJSON, func() {
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Fprintln(cl.out, string(b))
		})
	case "set":
		return cl.registryPolicySet(args[1:])
	}
	return usageErr("unknown command registry policy %s\n\n%s", args[0], registryUsage)
}

func (cl *client) registryPolicySet(args []string) error {
	fs := flag.NewFlagSet("registry policy set", flag.ContinueOnError)
	quota := fs.Float64("quota-gib", 0, "what the project may store, in GiB (0 for no quota)")
	keepLast := fs.Int("keep-last", 0, "keep the newest N tagged images")
	keepTags := fs.String("keep-tags", "", "keep images with a tag matching these patterns, comma separated (v*,release-*)")
	untagged := fs.Int("untagged-days", 0, "delete untagged images older than this many days")
	immutable := fs.String("immutable-tags", "", "tag patterns that cannot be overwritten or deleted once pushed (v*)")
	pullFrom := fs.String("pull-from", "", "projects (patterns) whose runs may pull this project's repositories")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	given := visited(fs)
	if len(pos) > 1 || len(given) == 0 || (given["json"] && len(given) == 1) {
		return usageErr("launchway registry policy set [PROJECT|REPOSITORY] [--quota-gib N] [--keep-last N] [--keep-tags P,P] [--untagged-days N] [--immutable-tags P,P] [--pull-from P,P]")
	}
	split := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	body := map[string]any{}
	if given["quota-gib"] {
		body["quota_bytes"] = int64(*quota * (1 << 30))
	}
	if given["keep-last"] || given["keep-tags"] || given["untagged-days"] {
		body["retention"] = map[string]any{"keep_last": *keepLast, "keep_tags": split(*keepTags), "untagged_days": *untagged}
	}
	if given["immutable-tags"] {
		body["immutable_tags"] = split(*immutable)
	}
	if given["pull-from"] {
		body["pull_from"] = split(*pullFrom)
	}
	target := ""
	if len(pos) == 1 {
		target = pos[0]
	}
	if target == "" {
		body = map[string]any{"scope": "installation", "policy": body}
	}
	var v map[string]any
	if err := cl.call("PUT", registryPolicyPath(target), body, &v); err != nil {
		return err
	}
	return cl.print(v, *asJSON, func() {
		fmt.Fprintln(cl.out, "The registry policy is set. Check it with: launchway registry policy get "+target)
	})
}

func (cl *client) registryGC(args []string) error {
	fs := flag.NewFlagSet("registry gc", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "list what would be removed and remove nothing")
	last := fs.Bool("last", false, "show the last collection's report")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	type report struct {
		DryRun  bool  `json:"dry_run"`
		Uploads int   `json:"uploads"`
		Links   int   `json:"links"`
		Blobs   int   `json:"blobs"`
		Bytes   int64 `json:"bytes"`
		Strays  int   `json:"strays"`
	}
	var rep report
	if *last {
		var v struct {
			Ran    bool   `json:"ran"`
			Report report `json:"report"`
		}
		if err := cl.call("GET", "/api/v1/registry/gc", nil, &v); err != nil {
			return err
		}
		return cl.print(v, *asJSON, func() {
			if !v.Ran {
				fmt.Fprintln(cl.out, "No collection has run yet.")
				return
			}
			fmt.Fprintf(cl.out, "Last collection removed %d blobs (%s), %d links, %d idle uploads, %d stray objects.\n", v.Report.Blobs, byteText(v.Report.Bytes), v.Report.Links, v.Report.Uploads, v.Report.Strays)
		})
	}
	if err := cl.call("POST", "/api/v1/registry/gc", map[string]any{"dry_run": *dry}, &rep); err != nil {
		return err
	}
	return cl.print(rep, *asJSON, func() {
		verb := "Removed"
		if rep.DryRun {
			verb = "Would remove"
		}
		fmt.Fprintf(cl.out, "%s %d blobs (%s), %d links, %d idle uploads, %d stray objects.\n", verb, rep.Blobs, byteText(rep.Bytes), rep.Links, rep.Uploads, rep.Strays)
		if rep.DryRun {
			fmt.Fprintln(cl.out, "Nothing was removed; run without --dry-run to collect.")
		}
	})
}
