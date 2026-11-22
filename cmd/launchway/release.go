package main

// launchway release: releases and release management. The same routes the web
// interface and MCP use; the server decides who may and its refusals are
// printed as they are.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const releaseUsage = `Usage:
  launchway release list [--project P] [--state draft|candidate|published|yanked] [--json]
  launchway release show FLIGHT VERSION [--json]
  launchway release next --project P [--from REF] [--pre rc] [--json]
        propose the next version from the changes since the last release, with the reasoning
  launchway release notes --project P [--to REF] [--version V]   preview generated release notes
  launchway release create --project P --version V [--from REF | --adopt TAG] [--notes-file F | --generate-notes]
                           [--title T] [--run RUN] [--allow-lower]
        make a draft release; with --run the image digests of that Sortie are recorded
  launchway release edit FLIGHT VERSION [--notes-file F] [--title T] [--regenerate-notes]
  launchway release assets add FLIGHT VERSION --file PATH [--name N] | --run RUN [--artifact ID --path P] [--images]
  launchway release assets remove FLIGHT VERSION NAME
  launchway release assets get FLIGHT VERSION NAME [-o FILE]
  launchway release candidate FLIGHT VERSION
  launchway release publish FLIGHT VERSION [--public]       create the annotated tag and fix the release
  launchway release yank FLIGHT VERSION --reason TEXT       withdraw a release, with a reason
  launchway release delete FLIGHT VERSION                   delete a draft
  launchway release changelog FLIGHT VERSION [--base BRANCH]   propose the notes as a changelog pull request
  launchway release compare FLIGHT VERSION-A VERSION-B
  launchway release checklist FLIGHT VERSION --airfield A [--json]
  launchway release clearance FLIGHT VERSION --airfield A [--comment TEXT]
  launchway release promote FLIGHT VERSION --airfield A [--override REASON]
        start the Flight's pipeline at the release's tag with its version and image digest
  launchway release plan FLIGHT VERSION --airfield A --at TIME [--note TEXT]
  launchway release return --project P --airfield A --reason TEXT [--to DEPLOYMENT|VERSION] [--queue]
        return to base: the Airfield goes back to the release before, by the existing rollback
  launchway release matrix [--json]                          which release is in which Airfield
  launchway release calendar [--from T] [--to T] [--json]
  launchway release report [--project P] [--from T] [--to T] [--html FILE] [--json]
  launchway release settings FLIGHT [--scheme semver|calver|natural|created] [--tag-prefix P] [--changelog F] [--public=bool]
  launchway release installation [--public-downloads=bool]   whether this installation allows public downloads
  launchway release gate list FLIGHT
  launchway release gate set FLIGHT --airfield A [--checks] [--notes] [--no-findings] [--soak MINUTES] [--clearances N]
                             [--clearance-role operator|admin] [--previous AIRFIELD]
                             [--window-days mon,tue,...] [--window-from HH:MM] [--window-to HH:MM] [--window-zone ZONE]
  launchway release freeze list
  launchway release freeze create --to TIME --reason TEXT [--from TIME] [--project P] [--airfield A]
  launchway release freeze cancel ID
  launchway release formation list
  launchway release formation show NAME
  launchway release formation create NAME FLIGHT@VERSION FLIGHT@VERSION ... [--note TEXT] [--order dependencies]   in promotion order, or by the software graph's dependencies
  launchway release formation promote NAME --airfield A [--override REASON]
  launchway release formation advance NAME

Times are 2026-11-24T14:00:00Z or 2026-11-24. A published release never changes; a correction is a new release.
`

func cmdRelease(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Print(releaseUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).release(args)
	})
}

func (cl *client) showJSON(v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(cl.out, string(b))
	return nil
}

func pp(project string) string { return "/api/v1/projects/" + url.PathEscape(project) }

func (cl *client) relPath(project, version, rest string) string {
	return pp(project) + "/releases/" + url.PathEscape(version) + rest
}

func (cl *client) release(args []string) error {
	rest := args[1:]
	switch args[0] {
	case "list":
		return cl.releaseList(rest)
	case "show":
		return cl.releaseShow(rest)
	case "next":
		return cl.releaseNext(rest)
	case "notes":
		return cl.releaseNotes(rest)
	case "create":
		return cl.releaseCreate(rest)
	case "edit":
		return cl.releaseEdit(rest)
	case "assets":
		return cl.releaseAssets(rest)
	case "candidate", "publish", "yank", "delete", "changelog", "clearance", "promote", "plan", "checklist":
		return cl.releaseAction(args[0], rest)
	case "compare":
		return cl.releaseCompare(rest)
	case "return":
		return cl.releaseReturn(rest)
	case "matrix":
		return cl.releaseMatrix(rest)
	case "calendar":
		return cl.releaseCalendar(rest)
	case "report":
		return cl.releaseReport(rest)
	case "settings":
		return cl.releaseSettings(rest)
	case "installation":
		return cl.releaseInstallation(rest)
	case "gate":
		return cl.releaseGate(rest)
	case "freeze":
		return cl.releaseFreeze(rest)
	case "formation":
		return cl.releaseFormation(rest)
	}
	return usageErr("unknown command release %s\n\n%s", args[0], releaseUsage)
}

func (cl *client) releaseList(args []string) error {
	fs := flag.NewFlagSet("release list", flag.ContinueOnError)
	project := fs.String("project", "", "only this Flight")
	state := fs.String("state", "", "draft, candidate, published or yanked")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var list []map[string]any
	if err := cl.call("GET", "/api/v1/releases?"+url.Values{"project": {*project}, "state": {*state}}.Encode(), nil, &list); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(cl.out, "no releases")
		return nil
	}
	for _, r := range list {
		fmt.Fprintf(cl.out, "%-20s %-16s %-10s %s  %s\n", r["project"], r["version"], r["state"], str(r["sha"], 8), r["tag"])
	}
	return nil
}

func str(v any, n int) string {
	s, _ := v.(string)
	if n > 0 && len(s) > n {
		s = s[:n]
	}
	return s
}

func (cl *client) two(fsName string, args []string, fs *flag.FlagSet) (project, version string, err error) {
	pos, err := parseArgs(fs, args)
	if err != nil {
		return "", "", err
	}
	if len(pos) != 2 {
		return "", "", usageErr("%s takes a Flight and a version: launchway release %s FLIGHT VERSION\n\n%s", fsName, fsName, releaseUsage)
	}
	return pos[0], pos[1], nil
}

func (cl *client) releaseShow(args []string) error {
	fs := flag.NewFlagSet("release show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	p, v, err := cl.two("show", args, fs)
	if err != nil {
		return err
	}
	var view map[string]any
	if err := cl.call("GET", cl.relPath(p, v, ""), nil, &view); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(view)
	}
	r, _ := view["release"].(map[string]any)
	built := "no Sortie recorded"
	if n := num(r["run_number"]); n > 0 {
		built = fmt.Sprintf("Sortie #%.0f", n)
	}
	fmt.Fprintf(cl.out, "%s %s (%s)\n  commit %s  tag %s  built by %s\n", r["project"], r["version"], r["state"], str(r["sha"], 12), r["tag"], built)
	if r["yank_reason"] != nil {
		fmt.Fprintf(cl.out, "  withdrawn by %s: %s\n", r["yanked_by"], r["yank_reason"])
	}
	if a, ok := r["assets"].([]any); ok {
		for _, x := range a {
			m, _ := x.(map[string]any)
			fmt.Fprintf(cl.out, "  %s %-24s %s %s\n", m["kind"], m["name"], str(m["sha256"], 12), str(m["digest"], 19))
		}
	}
	fmt.Fprintf(cl.out, "\n%s\n", r["notes"])
	return nil
}

func (cl *client) releaseNext(args []string) error {
	fs := flag.NewFlagSet("release next", flag.ContinueOnError)
	project := fs.String("project", "", "the Flight")
	from := fs.String("from", "", "the branch, tag or commit to release")
	pre := fs.String("pre", "", "propose a pre-release with this label, such as rc")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *project == "" {
		return usageErr("say which Flight: --project P\n\n%s", releaseUsage)
	}
	var out struct {
		Proposal struct {
			Next, Summary, Last string
			Bump                string
			Warnings            []string
			Reasons             []struct {
				Change struct{ Title, Kind string }
				Bump   string
				Why    string
			}
		}
	}
	if err := cl.call("GET", pp(*project)+"/releases/next?"+url.Values{"from": {*from}, "pre": {*pre}}.Encode(), nil, &out); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(out)
	}
	p := out.Proposal
	fmt.Fprintln(cl.out, p.Summary)
	for _, r := range p.Reasons {
		fmt.Fprintf(cl.out, "  %-5s %s (%s)\n", r.Bump, r.Change.Title, r.Why)
	}
	for _, w := range p.Warnings {
		fmt.Fprintln(cl.out, "  note:", w)
	}
	if p.Next != "" {
		fmt.Fprintf(cl.out, "Proposed: %s. You confirm or choose another: launchway release create --project %s --version %s\n", p.Next, *project, p.Next)
	}
	return nil
}

func (cl *client) releaseNotes(args []string) error {
	fs := flag.NewFlagSet("release notes", flag.ContinueOnError)
	project := fs.String("project", "", "the Flight")
	to := fs.String("to", "", "the branch, tag or commit")
	version := fs.String("version", "", "the version the notes are for")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *project == "" {
		return usageErr("say which Flight: --project P")
	}
	var out struct{ Notes struct{ Markdown string } }
	if err := cl.call("GET", pp(*project)+"/releases/notes?"+url.Values{"to": {*to}, "version": {*version}}.Encode(), nil, &out); err != nil {
		return err
	}
	fmt.Fprint(cl.out, out.Notes.Markdown)
	return nil
}

func readFlagFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the file the person named on their own command line
	return string(b), err
}

func (cl *client) releaseCreate(args []string) error {
	fs := flag.NewFlagSet("release create", flag.ContinueOnError)
	project := fs.String("project", "", "the Flight")
	version := fs.String("version", "", "the version")
	from := fs.String("from", "", "the branch, tag or commit to release")
	adopt := fs.String("adopt", "", "an existing annotated tag to release")
	notesFile := fs.String("notes-file", "", "a Markdown file of notes")
	gen := fs.Bool("generate-notes", false, "write the notes from the changes since the last release")
	title := fs.String("title", "", "a title")
	run := fs.String("run", "", "the Sortie that built it")
	lower := fs.Bool("allow-lower", false, "allow a version lower than the highest (a maintenance branch)")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *project == "" || *version == "" {
		return usageErr("say the Flight and version: --project P --version V\n\n%s", releaseUsage)
	}
	notes, err := readFlagFile(*notesFile)
	if err != nil {
		return usageErr("%v", err)
	}
	var out map[string]any
	if err := cl.call("POST", pp(*project)+"/releases", map[string]any{"version": *version, "from": *from, "adopt": *adopt, "notes": notes, "generate_notes": *gen,
		"title": *title, "run_id": *run, "allow_lower": *lower}, &out); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(out)
	}
	r, _ := out["release"].(map[string]any)
	fmt.Fprintf(cl.out, "Draft %s %s at %s.\n", r["project"], r["version"], str(r["sha"], 8))
	return nil
}

func (cl *client) releaseEdit(args []string) error {
	fs := flag.NewFlagSet("release edit", flag.ContinueOnError)
	notesFile := fs.String("notes-file", "", "a Markdown file of notes")
	title := fs.String("title", "", "a title")
	regen := fs.Bool("regenerate-notes", false, "write the notes again from the changes")
	p, v, err := cl.two("edit", args, fs)
	if err != nil {
		return err
	}
	if *regen {
		return cl.call("POST", cl.relPath(p, v, "/notes"), map[string]any{}, nil)
	}
	body := map[string]any{}
	if *notesFile != "" {
		n, err := readFlagFile(*notesFile)
		if err != nil {
			return usageErr("%v", err)
		}
		body["notes"] = n
	}
	if *title != "" {
		body["title"] = *title
	}
	if len(body) == 0 {
		return usageErr("say what to change: --notes-file, --title or --regenerate-notes")
	}
	if err := cl.call("PATCH", cl.relPath(p, v, ""), body, nil); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "Saved.")
	return nil
}

func (cl *client) releaseAssets(args []string) error {
	if len(args) == 0 {
		return usageErr("release assets add|remove|get\n\n%s", releaseUsage)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("release assets add", flag.ContinueOnError)
		file := fs.String("file", "", "a file to upload")
		name := fs.String("name", "", "the name it has in the release (default the file's name)")
		run := fs.String("run", "", "a Sortie whose Cargo or images to attach")
		art := fs.String("artifact", "", "an artifact of the Sortie (with --path)")
		path := fs.String("path", "", "a file of the artifact")
		images := fs.Bool("images", false, "record the Sortie's image digests, SBOM and scans")
		p, v, err := cl.two("assets add", args[1:], fs)
		if err != nil {
			return err
		}
		if *file != "" {
			return cl.upload(p, v, *file, *name)
		}
		if *run == "" {
			return usageErr("say what to add: --file PATH, or --run RUN with --artifact and --path, or --images")
		}
		body := map[string]any{"run_id": *run, "images": *images}
		if *art != "" {
			body["files"] = []map[string]string{{"artifact": *art, "path": *path, "name": *name}}
		}
		var added []map[string]any
		if err := cl.call("POST", cl.relPath(p, v, "/assets"), body, &added); err != nil {
			return err
		}
		for _, a := range added {
			fmt.Fprintf(cl.out, "added %s %s\n", a["kind"], a["name"])
		}
		return nil
	case "remove":
		if len(args) != 4 {
			return usageErr("launchway release assets remove FLIGHT VERSION NAME")
		}
		return cl.call("DELETE", cl.relPath(args[1], args[2], "/assets/"+url.PathEscape(args[3])), nil, nil)
	case "get":
		fs := flag.NewFlagSet("release assets get", flag.ContinueOnError)
		out := fs.String("o", "", "the file to write (default the asset's name)")
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 3 {
			return usageErr("launchway release assets get FLIGHT VERSION NAME [-o FILE]")
		}
		return cl.download(cl.relPath(pos[0], pos[1], "/assets/"+url.PathEscape(pos[2])), nonEmptyStr(*out, pos[2]))
	}
	return usageErr("unknown command release assets %s\n\n%s", args[0], releaseUsage)
}

func (cl *client) upload(project, version, file, name string) error {
	f, err := os.Open(file) //nolint:gosec // G304: the file the person named on their own command line
	if err != nil {
		return usageErr("%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return usageErr("%v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, cl.c.Server+cl.relPath(project, version, "/assets/"+url.PathEscape(nonEmptyStr(name, filepath.Base(file)))), f) //nolint:gosec // G704: the user's own configured server
	if err != nil {
		return usageErr("%v", err)
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("X-Launchway-CSRF", "1")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := cl.http.Do(req) //nolint:gosec // G704: the CLI calls the Launchway server the user configured
	if err != nil {
		return apiErr("cannot reach %s: %v", cl.c.Server, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		return apiErr("%s", nonEmptyStr(e.Error, resp.Status))
	}
	var a map[string]any
	_ = json.Unmarshal(data, &a)
	fmt.Fprintf(cl.out, "added %s (%.0f bytes, SHA-256 %s)\n", a["name"], num(a["size"]), a["sha256"])
	return nil
}

func (cl *client) download(path, dest string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, cl.c.Server+path, nil) //nolint:gosec // G704: the user's own configured server
	if err != nil {
		return usageErr("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	resp, err := cl.http.Do(req) //nolint:gosec // G704: the CLI calls the Launchway server the user configured
	if err != nil {
		return apiErr("cannot reach %s: %v", cl.c.Server, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		return apiErr("%s", nonEmptyStr(e.Error, resp.Status))
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the file the person named
	if err != nil {
		return usageErr("%v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return apiErr("%v", err)
	}
	fmt.Fprintf(cl.out, "wrote %s (SHA-256 %s)\n", dest, resp.Header.Get("X-Launchway-SHA256"))
	return nil
}

func (cl *client) releaseAction(name string, args []string) error {
	fs := flag.NewFlagSet("release "+name, flag.ContinueOnError)
	airfield := fs.String("airfield", "", "the Airfield")
	override := fs.String("override", "", "administrators: why the checklist is overridden")
	reason := fs.String("reason", "", "why")
	comment := fs.String("comment", "", "a comment")
	at := fs.String("at", "", "a time")
	note := fs.String("note", "", "a note")
	base := fs.String("base", "", "the branch")
	public := fs.Bool("public", false, "make the release downloadable without signing in (when allowed)")
	asJSON := fs.Bool("json", false, "print JSON")
	p, v, err := cl.two(name, args, fs)
	if err != nil {
		return err
	}
	var out map[string]any
	switch name {
	case "candidate":
		err = cl.call("POST", cl.relPath(p, v, "/candidate"), map[string]any{}, &out)
	case "publish":
		err = cl.call("POST", cl.relPath(p, v, "/publish"), map[string]any{"public": *public}, &out)
	case "yank":
		if strings.TrimSpace(*reason) == "" {
			return usageErr("say why: --reason \"...\" (it is recorded with the release)")
		}
		err = cl.call("POST", cl.relPath(p, v, "/yank"), map[string]any{"reason": *reason}, &out)
	case "delete":
		err = cl.call("DELETE", cl.relPath(p, v, ""), nil, &out)
	case "changelog":
		err = cl.call("POST", cl.relPath(p, v, "/changelog"), map[string]any{"base": *base}, &out)
	case "clearance":
		err = cl.call("POST", cl.relPath(p, v, "/clearances"), map[string]any{"airfield": *airfield, "comment": *comment}, &out)
	case "promote":
		err = cl.call("POST", cl.relPath(p, v, "/promote"), map[string]any{"airfield": *airfield, "override": *override}, &out)
	case "plan":
		err = cl.call("POST", cl.relPath(p, v, "/plan"), map[string]any{"airfield": *airfield, "at": *at, "note": *note}, &out)
	case "checklist":
		if *airfield == "" {
			return usageErr("say which Airfield: --airfield A")
		}
		err = cl.call("GET", cl.relPath(p, v, "/checklist?airfield="+url.QueryEscape(*airfield)), nil, &out)
	}
	if err != nil {
		return err
	}
	if *asJSON || name == "checklist" && *asJSON {
		return cl.showJSON(out)
	}
	if name == "checklist" {
		ck, _ := out["checklist"].(map[string]any)
		items, _ := ck["items"].([]any)
		for _, x := range items {
			m, _ := x.(map[string]any)
			fmt.Fprintf(cl.out, "  %-8s %s %s\n", m["status"], m["label"], m["detail"])
		}
		fmt.Fprintf(cl.out, "ready: %v\n", ck["ready"])
		return nil
	}
	if name == "promote" {
		run, _ := out["run"].(map[string]any)
		fmt.Fprintf(cl.out, "Promoting %s %s to %s: run #%v %s\n", p, v, *airfield, run["number"], run["id"])
		return nil
	}
	fmt.Fprintf(cl.out, "%s: done.\n", name)
	if pr, ok := out["proposal"].(map[string]any); ok {
		fmt.Fprintf(cl.out, "branch %v; open a pull request into %v %v\n", pr["branch"], pr["base"], pr["pull_request_url"])
	}
	return nil
}

func (cl *client) releaseCompare(args []string) error {
	fs := flag.NewFlagSet("release compare", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 3 {
		return usageErr("launchway release compare FLIGHT VERSION-A VERSION-B")
	}
	var out map[string]any
	if err := cl.call("GET", pp(pos[0])+"/releases/compare?"+url.Values{"a": {pos[1]}, "b": {pos[2]}}.Encode(), nil, &out); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "%s → %s\n%s\n", out["older"], out["newer"], out["notes"])
	if f, ok := out["files"].(map[string]any); ok {
		fmt.Fprintf(cl.out, "files: %v\n", f["stat"])
		if m, ok := f["manifests"].([]any); ok {
			for _, x := range m {
				fmt.Fprintf(cl.out, "  deployment configuration changed: %v\n", x)
			}
		}
	}
	return nil
}

func (cl *client) releaseReturn(args []string) error {
	fs := flag.NewFlagSet("release return", flag.ContinueOnError)
	project := fs.String("project", "", "the Flight")
	airfield := fs.String("airfield", "", "the Airfield")
	to := fs.String("to", "", "a deployment or version to return to (default the previous)")
	reason := fs.String("reason", "", "why; required")
	queue := fs.Bool("queue", false, "wait behind a deployment that holds the lock")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *project == "" || *airfield == "" || strings.TrimSpace(*reason) == "" {
		return usageErr("say the Flight, the Airfield and why: --project P --airfield A --reason \"...\"")
	}
	var run map[string]any
	if err := cl.call("POST", pp(*project)+"/release-returns", map[string]any{"airfield": *airfield, "to": *to, "reason": *reason, "queue": *queue}, &run); err != nil {
		return err
	}
	fmt.Fprintf(cl.out, "Returning %s in %s to base: run #%v %v\n", *project, *airfield, run["number"], run["id"])
	return nil
}

func (cl *client) releaseMatrix(args []string) error {
	fs := flag.NewFlagSet("release matrix", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var m struct {
		Airfields []string
		Rows      []struct {
			Project string
			Latest  string
			Cells   []struct{ Version, Health string }
		}
	}
	if err := cl.call("GET", "/api/v1/release-matrix", nil, &m); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(m)
	}
	fmt.Fprintf(cl.out, "%-20s %-12s", "project", "latest")
	for _, a := range m.Airfields {
		fmt.Fprintf(cl.out, " %-18s", a)
	}
	fmt.Fprintln(cl.out)
	for _, r := range m.Rows {
		fmt.Fprintf(cl.out, "%-20s %-12s", r.Project, r.Latest)
		for _, c := range r.Cells {
			fmt.Fprintf(cl.out, " %-18s", nonEmptyStr(c.Version+" "+c.Health, "-"))
		}
		fmt.Fprintln(cl.out)
	}
	return nil
}

func (cl *client) releaseCalendar(args []string) error {
	fs := flag.NewFlagSet("release calendar", flag.ContinueOnError)
	from := fs.String("from", "", "from")
	to := fs.String("to", "", "to")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var out struct {
		Entries []struct {
			At          int64
			Kind, Title string
			State       string
		}
	}
	if err := cl.call("GET", "/api/v1/release-calendar?"+url.Values{"from": {*from}, "to": {*to}}.Encode(), nil, &out); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(out)
	}
	for _, e := range out.Entries {
		fmt.Fprintf(cl.out, "%s  %-12s %s %s\n", stamp(e.At), e.Kind, e.Title, e.State)
	}
	return nil
}

func (cl *client) releaseReport(args []string) error {
	fs := flag.NewFlagSet("release report", flag.ContinueOnError)
	project := fs.String("project", "", "only this Flight")
	from := fs.String("from", "", "from")
	to := fs.String("to", "", "to")
	htmlFile := fs.String("html", "", "write the report as an HTML file")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{"project": {*project}, "from": {*from}, "to": {*to}}
	if *htmlFile != "" {
		q.Set("format", "html")
		return cl.download("/api/v1/release-report?"+q.Encode(), *htmlFile)
	}
	var rep struct {
		Measures struct {
			Launches, Failures                                                 int
			FrequencyPerWeek, LeadTimeMedianSec, ChangeFailureRate, RestoreSec float64
		}
		Raw map[string]any `json:"-"`
	}
	var raw map[string]any
	if err := cl.call("GET", "/api/v1/release-report?"+q.Encode(), nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(raw)
	}
	m, _ := raw["measures"].(map[string]any)
	fmt.Fprintf(cl.out, "launches %v, failures %v, frequency %.2f a week, lead time (median) %.1f h, change failure rate %.0f%%, time to restore (median) %.1f h\n",
		m["launches"], m["failures"], num(m["frequency_per_week"]), num(m["lead_time_median_s"])/3600, num(m["change_failure_rate"])*100, num(m["time_to_restore_median_s"])/3600)
	_ = rep
	return nil
}

func num(v any) float64 { f, _ := v.(float64); return f }

func (cl *client) releaseSettings(args []string) error {
	fs := flag.NewFlagSet("release settings", flag.ContinueOnError)
	scheme := fs.String("scheme", "", "semver, calver, natural or created")
	prefix := fs.String("tag-prefix", "", "the tag prefix, such as v")
	changelog := fs.String("changelog", "", "the changelog file")
	public := fs.Bool("public", false, "allow public downloads of this Flight's published releases")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway release settings FLIGHT [flags]")
	}
	var cur struct{ Settings map[string]any }
	if err := cl.call("GET", pp(pos[0])+"/release-settings", nil, &cur); err != nil {
		return err
	}
	set := fs.NFlag() > 0
	if !set {
		return cl.showJSON(cur)
	}
	body := cur.Settings
	if *scheme != "" {
		body["scheme"] = *scheme
	}
	if *prefix != "" {
		body["tag_prefix"] = *prefix
	}
	if *changelog != "" {
		body["changelog"] = *changelog
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["public"] {
		body["public"] = *public
	}
	var out map[string]any
	if err := cl.call("PUT", pp(pos[0])+"/release-settings", body, &out); err != nil {
		return err
	}
	return cl.showJSON(out)
}

func (cl *client) releaseInstallation(args []string) error {
	fs := flag.NewFlagSet("release installation", flag.ContinueOnError)
	pub := fs.Bool("public-downloads", false, "allow public downloads of releases that Flights and releases ask to make public")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NFlag() == 0 {
		var out map[string]any
		if err := cl.call("GET", "/api/v1/release-installation", nil, &out); err != nil {
			return err
		}
		return cl.showJSON(out)
	}
	var out map[string]any
	if err := cl.call("PUT", "/api/v1/release-installation", map[string]any{"public_downloads": *pub}, &out); err != nil {
		return err
	}
	return cl.showJSON(out)
}

func (cl *client) releaseGate(args []string) error {
	if len(args) == 0 {
		return usageErr("release gate list|set\n\n%s", releaseUsage)
	}
	switch args[0] {
	case "list":
		if len(args) != 2 {
			return usageErr("launchway release gate list FLIGHT")
		}
		var out any
		if err := cl.call("GET", pp(args[1])+"/release-gates", nil, &out); err != nil {
			return err
		}
		return cl.showJSON(out)
	case "set":
		fs := flag.NewFlagSet("release gate set", flag.ContinueOnError)
		airfield := fs.String("airfield", "", "the Airfield")
		checks := fs.Bool("checks", false, "checks must be green on the release's commit")
		notes := fs.Bool("notes", false, "the release must have notes")
		nofind := fs.Bool("no-findings", false, "no blocking findings may be open")
		soak := fs.Int("soak", 0, "minutes in the previous Airfield")
		nClear := fs.Int("clearances", 0, "how many clearances")
		role := fs.String("clearance-role", "", "operator or admin (default both)")
		prev := fs.String("previous", "", "the Airfield it must be in first")
		days := fs.String("window-days", "", "mon,tue,...")
		wfrom := fs.String("window-from", "", "HH:MM")
		wto := fs.String("window-to", "", "HH:MM")
		wzone := fs.String("window-zone", "", "a time zone")
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *airfield == "" {
			return usageErr("launchway release gate set FLIGHT --airfield A [flags]")
		}
		g := map[string]any{"require_checks": *checks, "require_notes": *notes, "require_no_blocking_findings": *nofind, "soak_minutes": *soak, "clearances": *nClear, "previous_airfield": *prev}
		if *role != "" {
			g["clearance_roles"] = []string{*role}
		}
		if *days != "" || *wfrom != "" || *wzone != "" {
			g["window"] = map[string]any{"days": splitList(*days), "from": *wfrom, "to": *wto, "zone": *wzone}
		}
		var out any
		if err := cl.call("PUT", pp(pos[0])+"/release-gates/"+url.PathEscape(*airfield), g, &out); err != nil {
			return err
		}
		return cl.showJSON(out)
	}
	return usageErr("unknown command release gate %s", args[0])
}

func (cl *client) releaseFreeze(args []string) error {
	if len(args) == 0 {
		return usageErr("release freeze list|create|cancel\n\n%s", releaseUsage)
	}
	switch args[0] {
	case "list":
		var out []map[string]any
		if err := cl.call("GET", "/api/v1/release-freezes", nil, &out); err != nil {
			return err
		}
		for _, f := range out {
			fmt.Fprintf(cl.out, "%v  %s to %s  %v / %v  %v\n", f["id"], stamp(int64(num(f["from"]))), stamp(int64(num(f["to"]))), nonEmptyStr(str(f["project"], 0), "every Flight"), nonEmptyStr(str(f["airfield"], 0), "every Airfield"), f["reason"])
		}
		return nil
	case "create":
		fs := flag.NewFlagSet("release freeze create", flag.ContinueOnError)
		project := fs.String("project", "", "a Flight (default every Flight)")
		airfield := fs.String("airfield", "", "an Airfield (default every Airfield)")
		from := fs.String("from", "", "from (default now)")
		to := fs.String("to", "", "until")
		reason := fs.String("reason", "", "why")
		if _, err := parseArgs(fs, args[1:]); err != nil {
			return err
		}
		var out map[string]any
		if err := cl.call("POST", "/api/v1/release-freezes", map[string]any{"project": *project, "airfield": *airfield, "from": *from, "to": *to, "reason": *reason}, &out); err != nil {
			return err
		}
		fmt.Fprintf(cl.out, "Freeze %v made.\n", out["id"])
		return nil
	case "cancel":
		if len(args) != 2 {
			return usageErr("launchway release freeze cancel ID")
		}
		return cl.call("POST", "/api/v1/release-freezes/"+url.PathEscape(args[1])+"/cancel", map[string]any{}, nil)
	}
	return usageErr("unknown command release freeze %s", args[0])
}

func (cl *client) releaseFormation(args []string) error {
	if len(args) == 0 {
		return usageErr("release formation list|show|create|promote|advance\n\n%s", releaseUsage)
	}
	var out any
	switch args[0] {
	case "list":
		if err := cl.call("GET", "/api/v1/release-formations", nil, &out); err != nil {
			return err
		}
	case "show":
		if len(args) != 2 {
			return usageErr("launchway release formation show NAME")
		}
		if err := cl.call("GET", "/api/v1/release-formations/"+url.PathEscape(args[1]), nil, &out); err != nil {
			return err
		}
	case "create":
		fs := flag.NewFlagSet("release formation create", flag.ContinueOnError)
		note := fs.String("note", "", "a note")
		order := fs.String("order", "", `"dependencies": put the releases in the software graph's dependency order, providers first`)
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) < 3 {
			return usageErr("launchway release formation create NAME FLIGHT@VERSION FLIGHT@VERSION ...")
		}
		if err := cl.call("POST", "/api/v1/release-formations", map[string]any{"name": pos[0], "note": *note, "releases": pos[1:], "order": *order}, &out); err != nil {
			return err
		}
	case "promote":
		fs := flag.NewFlagSet("release formation promote", flag.ContinueOnError)
		airfield := fs.String("airfield", "", "the Airfield")
		override := fs.String("override", "", "administrators: why the checklists are overridden")
		pos, err := parseArgs(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || *airfield == "" {
			return usageErr("launchway release formation promote NAME --airfield A")
		}
		if err := cl.call("POST", "/api/v1/release-formations/"+url.PathEscape(pos[0])+"/promote", map[string]any{"airfield": *airfield, "override": *override}, &out); err != nil {
			return err
		}
	case "advance":
		if len(args) != 2 {
			return usageErr("launchway release formation advance NAME")
		}
		if err := cl.call("POST", "/api/v1/release-formations/"+url.PathEscape(args[1])+"/advance", map[string]any{}, &out); err != nil {
			return err
		}
	default:
		return usageErr("unknown command release formation %s", args[0])
	}
	return cl.showJSON(out)
}
