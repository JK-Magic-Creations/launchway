package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/preview"
	"github.com/jk-magic-creations/launchway/internal/depupdate/propose"
	"github.com/jk-magic-creations/launchway/internal/depupdate/service"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

const depsUsage = `Usage:
  launchway deps list [-R OWNER/NAME] [--ecosystem E] [--status S] [--json]
                                    what a repository depends on: version in use, newest version, what the
                                    policy would move to, age, known vulnerabilities, why nothing is proposed
  launchway deps check [--dir DIR] [--osv-dir DIR | --osv-url URL] [--sarif FILE] [--json]
                                    look for known vulnerabilities in the dependencies of a working copy; with
                                    --sarif write a report a pipeline publishes to the Security tab; exits 1
                                    when one is found
  launchway deps update NAME... [-R OWNER/NAME] [--dry-run]
                                    open the pull request for these dependencies now, outside the schedule
                                    (same as updates run --only; limits for security fixes still apply)
  launchway deps ignore NAME [--versions RANGE] [--kind patch|minor|major] [--reason TEXT] [--file PATH]
                                    add an ignore rule to .launchway/updates.yaml in this working copy
                                    (commit it to the default branch for it to apply)

  launchway updates preview [-R OWNER/NAME] [--json]
                                    what the updater would open now: pull requests, updates in place, closings,
                                    and what waits behind the limits, with the reason
  launchway updates run -R OWNER/NAME [--only NAME,NAME] [--dry-run] [--executor podman|docker|host]
                                    [--workdir DIR]
                                    open, update and close the updater's pull requests for a repository. Run it
                                    as a pipeline step on a runner with a token that may push branches and open
                                    pull requests: lock files are written by the ecosystem's tool in a container,
                                    never on the server. --dry-run changes nothing.
  launchway updates settings [-R OWNER/NAME | --dir DIR] [--json]
                                    the settings that apply: the defaults, then the levels above the repository,
                                    then .launchway/updates.yaml, and which level locked what

NAME is a dependency's name as the manifest writes it. Settings are the repository's file
.launchway/updates.yaml, read from the default branch only. Registries and vulnerability data come
from the server's LAUNCHWAY_UPDATES_* settings; on a runner the same variables name the mirrors.
Ecosystems available: Go modules, npm, Python, container images, workflow files. Coming: Maven and
Gradle, Cargo, .NET, Helm charts, Terraform providers.
`

func cmdDeps(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, depsUsage)
			return cliErr{exitUsage, ""}
		}
		switch args[0] {
		case "check":
			return depsCheck(args[1:])
		case "ignore":
			return depsIgnore(args[1:])
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		cl.http.Timeout = 5 * time.Minute
		switch args[0] {
		case "list":
			return cl.depsList(args[1:])
		case "update":
			return cl.updatesRun(onlyArgs(args[1:]))
		}
		return usageErr("unknown command deps %s\n\n%s", args[0], depsUsage)
	})
}

// onlyArgs turns "deps update A B -R x" into "--only A,B -R x".
func onlyArgs(args []string) []string {
	var names, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "-"):
			rest = append(rest, a)
			if (a == "-R" || a == "--workdir" || a == "--executor") && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		default:
			names = append(names, a)
		}
	}
	return append([]string{"--only", strings.Join(names, ",")}, rest...)
}

func cmdUpdates(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, depsUsage)
			return cliErr{exitUsage, ""}
		}
		if args[0] == "settings" && hasUpdatesFlag(args[1:], "--dir") {
			return updatesSettingsLocal(args[1:])
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		cl.http.Timeout = 10 * time.Minute
		switch args[0] {
		case "preview":
			return cl.updatesPreview(args[1:])
		case "run":
			return cl.updatesRun(args[1:])
		case "settings":
			return cl.updatesSettings(args[1:])
		}
		return usageErr("unknown command updates %s\n\n%s", args[0], depsUsage)
	})
}

func hasUpdatesFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// ---- the report, as the server sends it ----

type depReport struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	preview.Report
}

func (cl *client) report(args []string, name, path string) (depReport, *prFlags, error) {
	f := newPRFlags(name)
	eco := f.fs.String("ecosystem", "", "only this ecosystem")
	status := f.fs.String("status", "", "only this status (update, security, held, ignored, manual, vulnerable, up-to-date)")
	if err := f.parse(args); err != nil {
		return depReport{}, f, err
	}
	var rep depReport
	if err := cl.call("GET", f.base+path, nil, &rep); err != nil {
		return rep, f, err
	}
	if *eco != "" || *status != "" {
		var keep []plan.Update
		for _, u := range rep.Updates {
			if (*eco == "" || u.Dep.Ecosystem == *eco) && (*status == "" || u.Status == *status) {
				keep = append(keep, u)
			}
		}
		rep.Updates = keep
	}
	return rep, f, nil
}

func (cl *client) depsList(args []string) error {
	rep, f, err := cl.report(args, "deps list", "/dependencies")
	if err != nil {
		return err
	}
	return cl.print(rep, *f.asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DEPENDENCY\tECOSYSTEM\tCURRENT\tLATEST\tWANTED\tSTATUS\tVULNERABLE")
		for _, u := range rep.Updates {
			if !u.Dep.Direct {
				continue
			}
			v := ""
			for _, a := range u.Advisories {
				v += a.ID + "(" + a.Severity + ") "
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", u.Dep.Name, u.Dep.Ecosystem, dash(u.Dep.Current), dash(u.Latest), dash(u.Target), u.Status, strings.TrimSpace(v))
		}
		tw.Flush()
		fmt.Fprintf(cl.out, "\n%s at %s: %d dependencies; coming, not available yet: %s\n", rep.Repository, short7(rep.Commit), len(rep.Updates), strings.Join(rep.Coming, ", "))
	})
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short7(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func (cl *client) updatesPreview(args []string) error {
	rep, f, err := cl.report(args, "updates preview", "/updates/preview")
	if err != nil {
		return err
	}
	return cl.print(rep, *f.asJSON, func() { printDecisions(cl.out, rep.Report, rep.Policy) })
}

func (cl *client) updatesSettings(args []string) error {
	rep, f, err := cl.report(args, "updates settings", "/dependencies")
	if err != nil {
		return err
	}
	return cl.print(rep.Policy, *f.asJSON, func() { printUpdatePolicy(cl.out, rep.Policy, rep.FileError) })
}

func printUpdatePolicy(w interface{ Write([]byte) (int, error) }, p plan.Policy, fileErr string) {
	fmt.Fprintf(w, "updates: %v   security: %v   routine: %v\nschedule: %s   wait for new releases: %d days\n", p.Enabled, p.Security, p.Routine, plan.Describe(p.Schedule), p.CooldownDays)
	fmt.Fprintf(w, "limits: %d open, %d per run, %d per ecosystem (0: none)   auto-merge: %v\n", p.Limits.MaxOpen, p.Limits.MaxPerRun, p.Limits.MaxOpenPerEcosystem, p.AutoMerge.Enabled)
	var eco []string
	for e, v := range p.Ecosystems {
		if v.Enabled {
			eco = append(eco, e)
		}
	}
	sort.Strings(eco)
	fmt.Fprintf(w, "ecosystems: %s\n", strings.Join(eco, ", "))
	for k, by := range p.Locked {
		fmt.Fprintf(w, "locked: %s (by the %s level)\n", k, by)
	}
	for _, i := range p.Ignored {
		fmt.Fprintf(w, "not applied: %s\n", i)
	}
	if fileErr != "" {
		fmt.Fprintf(w, "settings file problem: %s\n", fileErr)
	}
}

func printDecisions(w interface{ Write([]byte) (int, error) }, r preview.Report, p plan.Policy) {
	if r.FileError != "" {
		fmt.Fprintf(w, "settings file problem: %s\n\n", r.FileError)
	}
	counts := map[string]int{}
	for _, d := range r.Decisions {
		counts[d.Action]++
		fmt.Fprintf(w, "%-7s %-52s %2d update(s)  %s\n", d.Action, d.Key, len(d.Updates), d.Why)
	}
	fmt.Fprintf(w, "\nwould open %d, update in place %d, close %d, keep %d; %d wait (schedule %s)\n",
		counts["open"], counts["update"], counts["close"], counts["keep"], counts["queued"], plan.Describe(p.Schedule))
}

// ---- deps check: local, read-only, for a pipeline ----

func depsCheck(args []string) error {
	fs := flag.NewFlagSet("deps check", flag.ContinueOnError)
	dir := fs.String("dir", ".", "the working copy")
	osvDir := fs.String("osv-dir", os.Getenv("LAUNCHWAY_UPDATES_OSV_DIR"), "a directory holding an OSV database mirror")
	osvURL := fs.String("osv-url", os.Getenv("LAUNCHWAY_UPDATES_OSV_URL"), "an OSV API address")
	sarif := fs.String("sarif", "", "write the findings as SARIF to this file")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	cfg := service.Config{OSVDir: *osvDir, OSVURL: *osvURL, Offline: os.Getenv("LAUNCHWAY_UPDATES_OFFLINE") == "1"}
	idx := cfg.Vulnerabilities(egress.Default.HTTPClient(30 * time.Second))
	if idx == nil {
		return usageErr("offline mode needs --osv-dir (a mirror of the OSV database)")
	}
	rep, err := preview.Run(context.Background(), preview.Input{FS: os.DirFS(*dir), OSV: idx, Sources: nil, Now: time.Now(), Immediate: true})
	if err != nil {
		return err
	}
	if rep.VulnError != "" {
		return apiErr("the vulnerability data could not be read: %s", rep.VulnError)
	}
	if *sarif != "" {
		doc, err := osv.SARIF(rep.Vulns)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*sarif, doc, 0o644); err != nil { //nolint:gosec // a report the person asked for
			return err
		}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(rep.Vulns, "", "  ")
		fmt.Println(string(b))
	} else {
		for _, m := range rep.Vulns {
			fix := "no fixed version yet"
			if m.FixedIn != "" {
				fix = "fixed in " + m.FixedIn
			}
			fmt.Printf("%-9s %s  %s %s in %s:%d  (%s)\n", m.Severity, m.ID, m.Dep.Name, m.Dep.Current, m.Dep.File, m.Dep.Line, fix)
		}
		fmt.Printf("%d known vulnerabilities in %d dependencies\n", len(rep.Vulns), len(rep.Updates))
	}
	if len(rep.Vulns) > 0 {
		return exitErr(1)
	}
	return nil
}

// ---- deps ignore: edits the file in the working copy ----

func depsIgnore(args []string) error {
	fs := flag.NewFlagSet("deps ignore", flag.ContinueOnError)
	versions := fs.String("versions", "", "only target versions in this range")
	kind := fs.String("kind", "", "only this kind of update (patch, minor, major)")
	reason := fs.String("reason", "", "why (shown in the dependency view)")
	file := fs.String("file", plan.FilePath, "the settings file")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway deps ignore NAME: which dependency?")
	}
	rule := map[string]any{"name": pos[0]}
	if *versions != "" {
		rule["versions"] = *versions
	}
	if *kind != "" {
		rule["kinds"] = []string{*kind}
	}
	if *reason != "" {
		rule["reason"] = *reason
	}
	b, _ := json.Marshal(rule)
	line := "  - " + string(b) + "\n"
	data, err := os.ReadFile(*file) //nolint:gosec // the person's own file
	switch {
	case err != nil && os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(*file), 0o755); err != nil {
			return err
		}
		data = []byte("version: 1\n")
	case err != nil:
		return err
	}
	text := string(data)
	if strings.Contains(text, "\nignore:") {
		i := strings.Index(text, "\nignore:")
		j := strings.Index(text[i+1:], "\n")
		text = text[:i+1+j+1] + line + text[i+1+j+1:]
	} else {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "ignore:\n" + line
	}
	if _, err := plan.ParseFile([]byte(text)); err != nil {
		return usageErr("the file would not be valid after this change: %v", err)
	}
	if err := os.WriteFile(*file, []byte(text), 0o644); err != nil { //nolint:gosec // the person's own file
		return err
	}
	fmt.Printf("added to %s: ignore %s. Commit it to the default branch for it to apply.\n", *file, pos[0])
	return nil
}

func updatesSettingsLocal(args []string) error {
	fs := flag.NewFlagSet("updates settings", flag.ContinueOnError)
	dir := fs.String("dir", ".", "the working copy")
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	rep, err := preview.Run(context.Background(), preview.Input{FS: os.DirFS(*dir), Now: time.Now()})
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(rep.Policy, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	printUpdatePolicy(os.Stdout, rep.Policy, rep.FileError)
	return nil
}

// ---- updates run: the updater, as a client of the server ----

type restRepo struct {
	DefaultBranch string `json:"default_branch"`
	Clone         struct {
		HTTPS string `json:"https"`
	} `json:"clone"`
}

type restHost struct {
	cl     *client
	base   string
	repo   restRepo
	branch string
}

func (h *restHost) DefaultBranch(context.Context) (string, error) { return h.repo.DefaultBranch, nil }

type restPull struct {
	Number       int    `json:"number"`
	SourceBranch string `json:"source_branch"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	State        string `json:"state"`
	MergedBy     string `json:"merged_by"`
}

func (h *restHost) pullsIn(state, prefix string) ([]restPull, error) {
	var out struct {
		Items []restPull `json:"items"`
	}
	if err := h.cl.call("GET", h.base+"/pulls?state="+state+"&limit=200", nil, &out); err != nil {
		return nil, err
	}
	var keep []restPull
	for _, p := range out.Items {
		if strings.HasPrefix(p.SourceBranch, strings.TrimRight(prefix, "/")+"/") {
			keep = append(keep, p)
		}
	}
	return keep, nil
}

func (h *restHost) Open(_ context.Context, prefix string) ([]propose.PR, error) {
	ps, err := h.pullsIn("open", prefix)
	var out []propose.PR
	for _, p := range ps {
		out = append(out, propose.PR{Number: p.Number, Branch: p.SourceBranch, Title: p.Title, Body: p.Body})
	}
	return out, err
}

func (h *restHost) Closed(_ context.Context, prefix string) ([]propose.PR, error) {
	ps, err := h.pullsIn("closed", prefix)
	var out []propose.PR
	for _, p := range ps {
		pr := propose.PR{Number: p.Number, Branch: p.SourceBranch, Title: p.Title, Body: p.Body}
		var conv struct {
			Comments []struct {
				Body string `json:"body"`
			} `json:"comments"`
		}
		if h.cl.call("GET", fmt.Sprintf("%s/pulls/%d/conversation", h.base, p.Number), nil, &conv) == nil {
			updater := false
			for _, c := range conv.Comments {
				if strings.HasPrefix(c.Body, propose.UpdaterClosing) {
					updater = true
				}
			}
			if updater {
				continue
			}
			if n := len(conv.Comments); n > 0 {
				pr.Reason = conv.Comments[n-1].Body
			}
		}
		out = append(out, pr)
	}
	return out, err
}

func (h *restHost) Create(_ context.Context, n propose.NewPR) (propose.PR, error) {
	var p restPull
	err := h.cl.call("POST", h.base+"/pulls", map[string]any{"title": n.Title, "body": n.Body, "source_branch": n.Branch, "target_branch": n.Base,
		"labels": n.Labels, "requested_reviewers": n.Reviewers, "assignees": n.Assignees}, &p)
	return propose.PR{Number: p.Number, Branch: n.Branch, Title: n.Title}, err
}

func (h *restHost) Update(_ context.Context, number int, title, body string) error {
	return h.cl.call("PATCH", fmt.Sprintf("%s/pulls/%d", h.base, number), map[string]any{"title": title, "body": body}, nil)
}

func (h *restHost) Close(_ context.Context, number int, comment string) error {
	if err := h.cl.call("POST", fmt.Sprintf("%s/pulls/%d/comments", h.base, number), map[string]any{"body": comment}, nil); err != nil {
		return err
	}
	return h.cl.call("PATCH", fmt.Sprintf("%s/pulls/%d", h.base, number), map[string]any{"state": "closed"}, nil)
}

func (cl *client) updatesRun(args []string) error {
	f := newPRFlags("updates run")
	only := f.fs.String("only", "", "only these dependencies, comma separated")
	dry := f.fs.Bool("dry-run", false, "change nothing: say what would be done")
	exe := f.fs.String("executor", "podman", "podman, docker or host (the host runs the tools directly: only for a machine that is itself the sandbox)")
	workdir := f.fs.String("workdir", "", "a directory for the clone and scratch files (default: a new temporary one)")
	if err := f.parse(args); err != nil {
		return err
	}
	var repo restRepo
	if err := cl.call("GET", f.base, nil, &repo); err != nil {
		return err
	}
	if repo.Clone.HTTPS == "" || repo.DefaultBranch == "" {
		return apiErr("%s has no clone address or no default branch yet", *f.repo)
	}
	var rep depReport
	if err := cl.call("GET", f.base+"/updates/preview", nil, &rep); err != nil {
		return err
	}
	decisions := rep.Decisions
	if *only != "" {
		decisions = onlyDecisions(rep.Decisions, splitList(*only))
	}
	work := *workdir
	if work == "" {
		d, err := os.MkdirTemp("", "launchway-updates-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)
		work = d
	}
	var ex executor.Executor
	switch *exe {
	case "host":
		ex = &executor.Host{}
	case "podman", "docker":
		bin, err := exec.LookPath(*exe)
		if err != nil {
			return usageErr("%s was not found on this machine; use --executor to choose another", *exe)
		}
		ex = &executor.Container{Bin: bin}
	default:
		return usageErr("--executor is podman, docker or host")
	}
	p := &propose.Proposer{
		Host:      &restHost{cl: cl, base: f.base, repo: repo},
		WS:        &propose.Workspace{URL: repo.Clone.HTTPS, Base: repo.DefaultBranch, Dir: filepath.Join(work, "clone"), Auth: &propose.Auth{Username: "launchway", Password: cl.c.Token}, Author: propose.Author{Name: "Launchway dependency updater", Email: "updates@launchway.invalid"}},
		Runner:    &propose.ExecRunner{Exec: ex, Meta: work, Env: registryEnv()},
		Policy:    rep.Policy,
		Toolchain: propose.FromPolicy(rep.Policy),
		Scratch:   work,
		DryRun:    *dry,
	}
	outcomes := p.Execute(context.Background(), decisions)
	failed := 0
	for _, o := range outcomes {
		line := fmt.Sprintf("%-7s %-50s %s", o.Event.Action, o.Decision.Key, o.Event.Detail)
		if o.Event.Number > 0 {
			line = fmt.Sprintf("%-7s #%-4d %-45s %s", o.Event.Action, o.Event.Number, o.Decision.Key, o.Event.Detail)
		}
		fmt.Fprintln(cl.out, line)
		if o.Err != nil {
			failed++
		}
	}
	if failed > 0 {
		return apiErr("%d of %d proposals failed", failed, len(outcomes))
	}
	return nil
}

// registryEnv gives the step container the registries this runner may use.
func registryEnv() map[string]string {
	env := map[string]string{}
	for k, v := range map[string]string{"GOPROXY": "LAUNCHWAY_UPDATES_GOPROXY", "npm_config_registry": "LAUNCHWAY_UPDATES_NPM_REGISTRY", "UV_INDEX_URL": "LAUNCHWAY_UPDATES_PYPI_SIMPLE"} {
		if x := os.Getenv(v); x != "" {
			env[k] = x
		}
	}
	// Anything else a registry needs (GONOSUMDB, GOFLAGS, an npm CA file) is passed on
	// by name: LAUNCHWAY_UPDATES_ENV_GOSUMDB=off becomes GOSUMDB=off in the step. Nothing
	// else of this machine's environment reaches the step, and never a credential.
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "LAUNCHWAY_UPDATES_ENV_") {
			env[strings.TrimPrefix(k, "LAUNCHWAY_UPDATES_ENV_")] = v
		}
	}
	return env
}

func onlyDecisions(ds []plan.Decision, names []string) []plan.Decision {
	var out []plan.Decision
	for _, d := range ds {
		for _, u := range d.Updates {
			if contains(names, u.Dep.Name) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
