package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/standards"
)

const standardsUsage = `Usage:
  launchway standards show PROJECT [--json]        the Standing orders that apply to a repository: each check, its state,
                                                   where the value comes from, and what a lock above holds
  launchway standards show --level LEVEL [--scope NAME] [--json]
                                                   the settings one level has (installation, organization, unit, project)
  launchway standards set --level LEVEL [--scope NAME] [KEY=VALUE ...] [--unset KEY ...] [--reason TEXT]
                                          [--lock KEY=REASON ...] [--unlock KEY ...]
                                                   change a level's settings; a level cannot weaken what a level above locked
  launchway standards explain PROJECT KEY          why a setting has the value it has: check ID, off, gate.fail_on, ...
  launchway standards coverage [--level LEVEL] [--scope NAME] [--json]
                                                   which repositories have which checks in effect
  launchway standards profile [--json]             every check Launchway can run, with its tool, license and defaults

KEY is a check (check.secrets=block, state off, advise or block), off=true (needs --reason: it switches Standing orders off for
the level), gate.fail_on (critical, high, medium or low), gate.require_fix, gate.default_branch (advise or block),
gate.incomplete (pass or fail), licenses.allow and licenses.deny (comma-separated names).
Settings change in the interface or here; a repository may also keep .launchway/standards.yaml for its own.

  launchway findings list PROJECT [--status open|fixed|accepted|all] [--severity S,S] [--check ID] [--q TEXT] [--oldest] [--json]
                                                   the findings the default branch has had: open, fixed or accepted
  launchway findings show PROJECT FINGERPRINT [--json]   one finding: what, where, why it matters, how to fix it
  launchway findings accept PROJECT FINGERPRINT --reason TEXT (--expires YYYY-MM-DD | --days N) [--false-positive]
                                                   accept a risk or mark a false positive; it ends on the date and is recorded
  launchway findings reopen PROJECT FINGERPRINT    end a decision, so the finding counts again
  launchway findings export PROJECT [--format json|csv|sarif] [--status S] [-o FILE]
  launchway findings inspect RUN [--standing new|existing|accepted] [--json]
                                                   what the Inspection of a run found and its verdict
`

func cmdStandards(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, standardsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "show":
			return cl.standardsShow(args[1:])
		case "set":
			return cl.standardsSet(args[1:])
		case "explain":
			return cl.standardsExplain(args[1:])
		case "coverage":
			return cl.standardsCoverage(args[1:])
		case "profile":
			return cl.standardsProfile(args[1:])
		}
		return usageErr("unknown command standards %s\n\n%s", args[0], standardsUsage)
	})
}

func cmdFindings(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, standardsUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "list":
			return cl.findingsList(args[1:])
		case "show":
			return cl.findingsShow(args[1:])
		case "accept":
			return cl.findingsAccept(args[1:])
		case "reopen":
			return cl.findingsReopen(args[1:])
		case "export":
			return cl.findingsExport(args[1:])
		case "inspect":
			return cl.findingsInspect(args[1:])
		}
		return usageErr("unknown command findings %s\n\n%s", args[0], standardsUsage)
	})
}

type stdLevelScope struct{ level, scope string }

func stdLevelFlags(fs *flag.FlagSet) *stdLevelScope {
	l := &stdLevelScope{}
	fs.StringVar(&l.level, "level", "", "installation, organization, unit or project")
	fs.StringVar(&l.scope, "scope", "", "the unit's or project's name")
	return l
}

func (l *stdLevelScope) query() string {
	q := url.Values{"level": {l.level}}
	if l.scope != "" {
		q.Set("scope", l.scope)
	}
	return q.Encode()
}

func (cl *client) standardsShow(args []string) error {
	fs := flag.NewFlagSet("standards show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	ls := stdLevelFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if ls.level != "" && len(pos) == 0 {
		var out struct {
			Level, Scope string
			Settings     standards.Settings `json:"settings"`
			Version      int                `json:"version"`
		}
		if err := cl.call("GET", "/api/v1/standards/settings?"+ls.query(), nil, &out); err != nil {
			return err
		}
		return cl.print(out, *asJSON, func() {
			raw, _ := json.MarshalIndent(out.Settings, "", "  ")
			fmt.Fprintf(cl.out, "Settings of %s %s, version %d:\n%s\n", out.Level, out.Scope, out.Version, raw)
		})
	}
	if len(pos) != 1 {
		return usageErr("launchway standards show PROJECT | --level LEVEL [--scope NAME] [--json]")
	}
	var p struct {
		Effective standards.Effective `json:"effective"`
		Commit    string              `json:"commit"`
		File      bool                `json:"file"`
		Problems  []string            `json:"file_problems"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/standards", nil, &p); err != nil {
		return err
	}
	return cl.print(p, *asJSON, func() { printEffective(cl, pos[0], p.Effective, p.File) })
}

func printEffective(cl *client, project string, e standards.Effective, hasFile bool) {
	fmt.Fprintf(cl.out, "Standing orders for %s (profile %s)\n", project, e.ProfileVersion)
	if e.Off.V {
		fmt.Fprintf(cl.out, "SWITCHED OFF by %s: %s\n", sourceWords(e.Off.From), e.OffReason)
	}
	for _, d := range e.Detected {
		fmt.Fprintf(cl.out, "  found: %s (%s)\n", d.Title, d.Because)
	}
	tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\nCHECK\tTOOL\tSTATE\tFROM\tWHY")
	for _, c := range e.Checks {
		why := ""
		if !c.Active {
			why = c.Reason
		}
		lock := ""
		if c.State.Locked != nil {
			lock = " (locked by " + c.State.Locked.Level + ")"
		}
		if c.Unavail != "" { // an unavailable check has no state to set: say so, with the reason
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Tool, "not available yet", "-", why)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s%s\t%s\n", c.ID, c.Tool, c.State.V, sourceWords(c.State.From), lock, why)
	}
	_ = tw.Flush()
	fmt.Fprintf(cl.out, "\nGate: blocks %s and above (%s), require a fix %v (%s), default branch %s (%s), a check that did not run: %s\n",
		e.FailOn.V, sourceWords(e.FailOn.From), e.RequireFix.V, sourceWords(e.RequireFix.From), e.DefaultBranch.V, sourceWords(e.DefaultBranch.From), e.Incomplete.V)
	if len(e.Deny.V) > 0 {
		fmt.Fprintf(cl.out, "Licenses denied: %s (%s)\n", strings.Join(e.Deny.V, ", "), sourceWords(e.Deny.From))
	}
	if len(e.Allow.V) > 0 {
		fmt.Fprintf(cl.out, "Licenses allowed: %s (%s)\n", strings.Join(e.Allow.V, ", "), sourceWords(e.Allow.From))
	}
	for _, x := range e.Exemptions {
		state := ""
		if x.Expired {
			state = " EXPIRED"
		}
		fmt.Fprintf(cl.out, "Exemption: %s until %s%s: %s\n", exemptionWhat(x.Ignore), x.Expires, state, x.Reason)
	}
	for _, n := range e.Notices {
		fmt.Fprintln(cl.out, "Note:", n)
	}
	if hasFile {
		fmt.Fprintln(cl.out, "The repository has its own", standards.FilePath)
	}
}

func exemptionWhat(i standards.Ignore) string {
	var parts []string
	for _, kv := range [][2]string{{"check", i.Check}, {"rule", i.Rule}, {"package", i.Package}, {"path", i.Path}, {"fingerprint", i.Fingerprint}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+" "+kv[1])
		}
	}
	return strings.Join(parts, ", ")
}

func sourceWords(s standards.Source) string {
	switch {
	case s.Kind == "default":
		return "the default"
	case s.Level == standards.LevelFile:
		return standards.FilePath
	case s.Scope != "":
		return s.Level + " " + s.Scope
	}
	return s.Level
}

func (cl *client) standardsProfile(args []string) error {
	fs := flag.NewFlagSet("standards profile", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	var p struct {
		Version string `json:"version"`
		Checks  []struct {
			ID, Title, Tool, Ecosystem, License, Kind string
			Default                                   string `json:"state_default"`
			Unavailable                               string `json:"unavailable"`
		} `json:"checks"`
	}
	if err := cl.call("GET", "/api/v1/standards/profile", nil, &p); err != nil {
		return err
	}
	return cl.print(p, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "Profile %s\nCHECK\tFOR\tTOOL\tLICENSE\tDEFAULT\n", p.Version)
		for _, c := range p.Checks {
			d := c.Default
			if c.Unavailable != "" {
				d = "not available yet"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Ecosystem, c.Tool, c.License, d)
		}
		_ = tw.Flush()
	})
}

func (cl *client) standardsExplain(args []string) error {
	fs := flag.NewFlagSet("standards explain", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway standards explain PROJECT KEY")
	}
	var p struct {
		Effective standards.Effective `json:"effective"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/standards", nil, &p); err != nil {
		return err
	}
	e, key := p.Effective, strings.TrimPrefix(pos[1], "check.")
	chain := func(title string, chain []standards.Link, locked *standards.LockInfo, why string) {
		fmt.Fprintln(cl.out, title)
		if why != "" {
			fmt.Fprintln(cl.out, "  "+why)
		}
		for _, l := range chain {
			note := ""
			switch {
			case l.Ignored != "":
				note = "   (ignored: " + l.Ignored + ")"
			case l.Overridden:
				note = "   (overridden)"
			}
			fmt.Fprintf(cl.out, "  %s: %s%s\n", sourceWords(l.Source), l.Display, note)
		}
		if locked != nil {
			fmt.Fprintf(cl.out, "  Locked by %s: %s. %s can change it.\n", locked.Level, locked.Reason, capitalFirst(locked.Who))
		}
	}
	for _, c := range e.Checks {
		if c.ID == key {
			chain(fmt.Sprintf("%s: %s", c.ID, c.State.V), c.State.Chain, c.State.Locked, c.Why)
			fmt.Fprintf(cl.out, "  Runs: %v. %s\n  Tool %s (%s license), budget %ds, needs: %s\n", c.Active, firstNonEmptyStr(c.Reason, "It applies because "+c.Because+"."), c.Tool, c.License, c.Budget, firstNonEmptyStr(c.Network, "no network"))
			return nil
		}
	}
	switch pos[1] {
	case "off":
		chain("off: "+fmt.Sprint(e.Off.V), e.Off.Chain, e.Off.Locked, "")
	case "gate.fail_on":
		chain("gate.fail_on: "+string(e.FailOn.V), e.FailOn.Chain, e.FailOn.Locked, "A finding at this severity or above blocks a change that introduces it.")
	case "gate.require_fix":
		chain("gate.require_fix: "+fmt.Sprint(e.RequireFix.V), e.RequireFix.Chain, e.RequireFix.Locked, "A vulnerability blocks only when a fixed version exists.")
	case "gate.default_branch":
		chain("gate.default_branch: "+string(e.DefaultBranch.V), e.DefaultBranch.Chain, e.DefaultBranch.Locked, "advise: the default branch shows its backlog and never fails; block: a finding on it fails its run.")
	case "gate.incomplete":
		chain("gate.incomplete: "+e.Incomplete.V, e.Incomplete.Chain, e.Incomplete.Locked, "What the verdict does when a check that should have run did not.")
	case "licenses.allow":
		chain("licenses.allow: "+strings.Join(e.Allow.V, ", "), e.Allow.Chain, e.Allow.Locked, "")
	case "licenses.deny":
		chain("licenses.deny: "+strings.Join(e.Deny.V, ", "), e.Deny.Chain, e.Deny.Locked, "")
	default:
		return usageErr("%q is not a check or a setting. Checks: %s. Settings: off, gate.fail_on, gate.require_fix, gate.default_branch, gate.incomplete, licenses.allow, licenses.deny",
			pos[1], strings.Join(standards.CheckIDs(), ", "))
	}
	return nil
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func capitalFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (cl *client) standardsCoverage(args []string) error {
	fs := flag.NewFlagSet("standards coverage", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	ls := stdLevelFlags(fs)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if ls.level == "" {
		ls.level = standards.LevelInstallation
	}
	var out struct {
		Projects []struct {
			Project string `json:"project"`
			Off     bool   `json:"off"`
			Checks  map[string]struct {
				State  string `json:"state"`
				Active bool   `json:"active"`
			} `json:"checks"`
			Open map[string]int `json:"open"`
		} `json:"projects"`
	}
	if err := cl.call("GET", "/api/v1/standards/coverage?"+ls.query(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PROJECT\tCHECKS IN EFFECT\tOPEN FINDINGS")
		for _, p := range out.Projects {
			var on []string
			for id, c := range p.Checks {
				if c.Active {
					on = append(on, id)
				}
			}
			sort.Strings(on)
			state := strings.Join(on, ", ")
			if p.Off {
				state = "SWITCHED OFF"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d critical, %d high, %d medium, %d low\n", p.Project, state, p.Open["critical"], p.Open["high"], p.Open["medium"], p.Open["low"])
		}
		_ = tw.Flush()
	})
}

func (cl *client) standardsSet(args []string) error {
	fs := flag.NewFlagSet("standards set", flag.ContinueOnError)
	ls := stdLevelFlags(fs)
	reason := fs.String("reason", "", "why (needed to switch Standing orders off)")
	var unset, lock, unlock multi
	fs.Var(&unset, "unset", "forget this level's value for a key; give it once for each key")
	fs.Var(&lock, "lock", "lock KEY=REASON: nobody below this level may weaken it; give it once for each")
	fs.Var(&unlock, "unlock", "remove the lock on a key")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if ls.level == "" {
		return usageErr("launchway standards set --level LEVEL [--scope NAME] KEY=VALUE ...")
	}
	var cur struct {
		Settings standards.Settings `json:"settings"`
		Version  int                `json:"version"`
	}
	if err := cl.call("GET", "/api/v1/standards/settings?"+ls.query(), nil, &cur); err != nil {
		return err
	}
	s := cur.Settings
	for _, kv := range pos {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return usageErr("%q: write KEY=VALUE", kv)
		}
		if err := applySetting(&s, k, v, *reason); err != nil {
			return err
		}
	}
	for _, k := range unset {
		if err := clearSetting(&s, k); err != nil {
			return err
		}
	}
	for _, l := range lock {
		k, why, _ := strings.Cut(l, "=")
		if s.Locks == nil {
			s.Locks = map[string]string{}
		}
		s.Locks[k] = why
	}
	for _, k := range unlock {
		delete(s.Locks, k)
	}
	body := map[string]any{"level": ls.level, "scope": ls.scope, "settings": s, "version": cur.Version}
	var out struct {
		Version int `json:"version"`
	}
	if err := cl.call("PUT", "/api/v1/standards/settings", body, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		fmt.Fprintf(cl.out, "Saved the settings of %s %s, version %d. Runs made from now on use them.\n", ls.level, ls.scope, out.Version)
	})
}

func applySetting(s *standards.Settings, k, v, reason string) error {
	b := func() (bool, error) {
		x, err := strconv.ParseBool(v)
		if err != nil {
			return false, usageErr("%s: write true or false", k)
		}
		return x, nil
	}
	switch {
	case k == "off":
		x, err := b()
		if err != nil {
			return err
		}
		s.Off, s.OffReason = &x, reason
	case strings.HasPrefix(k, "check."):
		if s.Checks == nil {
			s.Checks = map[string]standards.State{}
		}
		s.Checks[strings.TrimPrefix(k, "check.")] = standards.State(v)
	case k == standards.KeyFailOn:
		s.Gate.FailOn = findings.Severity(v)
	case k == standards.KeyRequireFix:
		x, err := b()
		if err != nil {
			return err
		}
		s.Gate.RequireFix = &x
	case k == standards.KeyDefaultBranch:
		s.Gate.DefaultBranch = standards.State(v)
	case k == standards.KeyIncomplete:
		s.Gate.Incomplete = v
	case k == standards.KeyAllow, k == standards.KeyDeny:
		list := []string{}
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				list = append(list, n)
			}
		}
		if k == standards.KeyAllow {
			s.Licenses.Allow = &list
		} else {
			s.Licenses.Deny = &list
		}
	default:
		return usageErr("%q is not a setting. See launchway standards for the keys", k)
	}
	return nil
}

func clearSetting(s *standards.Settings, k string) error {
	switch {
	case k == "off":
		s.Off, s.OffReason = nil, ""
	case strings.HasPrefix(k, "check."):
		delete(s.Checks, strings.TrimPrefix(k, "check."))
	case k == standards.KeyFailOn:
		s.Gate.FailOn = ""
	case k == standards.KeyRequireFix:
		s.Gate.RequireFix = nil
	case k == standards.KeyDefaultBranch:
		s.Gate.DefaultBranch = ""
	case k == standards.KeyIncomplete:
		s.Gate.Incomplete = ""
	case k == standards.KeyAllow:
		s.Licenses.Allow = nil
	case k == standards.KeyDeny:
		s.Licenses.Deny = nil
	case k == standards.KeyIgnore:
		s.Ignore = nil
	default:
		return usageErr("%q is not a setting. See launchway standards for the keys", k)
	}
	return nil
}

// ---- findings ----

func (cl *client) findingsList(args []string) error {
	fs := flag.NewFlagSet("findings list", flag.ContinueOnError)
	status := fs.String("status", "", "open (default), fixed, accepted or all")
	sev := fs.String("severity", "", "critical, high, medium, low (comma-separated)")
	check := fs.String("check", "", "only this check")
	q := fs.String("q", "", "search the title, rule, file and package")
	oldest := fs.Bool("oldest", false, "oldest first instead of most severe first")
	limit := fs.Int("limit", 100, "at most this many")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway findings list PROJECT [--status S] [--severity S] [--check ID] [--q TEXT] [--oldest] [--json]")
	}
	v := url.Values{"status": {*status}, "severity": {*sev}, "check": {*check}, "q": {*q}, "limit": {strconv.Itoa(*limit)}}
	if *oldest {
		v.Set("sort", "oldest")
	}
	var out engine.StandingList
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/findings?"+v.Encode(), nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "FINGERPRINT\tSEVERITY\tCHECK\tWHAT\tWHERE\tSINCE\tSTATUS")
		for _, f := range out.Findings {
			where := f.File
			if f.Line > 0 {
				where += ":" + strconv.Itoa(f.Line)
			}
			if f.Package != "" {
				where = f.Package + "@" + f.Version
				if f.FixedIn != "" {
					where += " (fixed in " + f.FixedIn + ")"
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", f.Fingerprint, f.Severity, f.Check, truncate(f.Title, 60), where, time.UnixMilli(f.FirstSeen).UTC().Format("2006-01-02"), f.Status)
		}
		_ = tw.Flush()
		fmt.Fprintf(cl.out, "%d shown of %d matching. Open now: %d (%d accepted, %d fixed).\n", len(out.Findings), out.Matched, out.Open, out.Accepted, out.Fixed)
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (cl *client) findingsShow(args []string) error {
	fs := flag.NewFlagSet("findings show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway findings show PROJECT FINGERPRINT [--json]")
	}
	var f engine.TrackedFinding
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/findings/"+url.PathEscape(pos[1]), nil, &f); err != nil {
		return err
	}
	return cl.print(f, *asJSON, func() {
		fmt.Fprintf(cl.out, "%s %s: %s\n", strings.ToUpper(f.Severity), f.Rule, f.Title)
		fmt.Fprintf(cl.out, "  Check %s, tool %s, status %s, first seen %s (run #%d)\n", f.Check, f.Tool, f.Status, time.UnixMilli(f.FirstSeen).UTC().Format("2006-01-02"), f.FirstRun)
		if f.File != "" {
			fmt.Fprintf(cl.out, "  Where: %s:%d\n", f.File, f.Line)
		}
		if f.Package != "" {
			fmt.Fprintf(cl.out, "  Package: %s %s, fixed in %s\n", f.Package, f.Version, firstNonEmptyStr(f.FixedIn, "no fixed version known"))
		}
		if f.Fix != "" {
			fmt.Fprintf(cl.out, "  How to fix: %s\n", f.Fix)
		}
		if f.Decision != nil {
			fmt.Fprintf(cl.out, "  Decision: %s by %s until %s: %s\n", f.Decision.Status, f.Decision.By, time.UnixMilli(f.Decision.ExpiresAt).UTC().Format("2006-01-02"), f.Decision.Reason)
		}
	})
}

func (cl *client) findingsAccept(args []string) error {
	fs := flag.NewFlagSet("findings accept", flag.ContinueOnError)
	reason := fs.String("reason", "", "why (at least 10 characters)")
	expires := fs.String("expires", "", "the date the decision ends, YYYY-MM-DD")
	days := fs.Int("days", 0, "or: it ends in this many days")
	fp := fs.Bool("false-positive", false, "mark it a false positive instead of an accepted risk")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || *reason == "" || (*expires == "") == (*days == 0) {
		return usageErr("launchway findings accept PROJECT FINGERPRINT --reason TEXT (--expires YYYY-MM-DD | --days N) [--false-positive]")
	}
	if *days > 0 {
		*expires = time.Now().UTC().AddDate(0, 0, *days).Format(standards.DateFormat)
	}
	status := standards.StatusAccepted
	if *fp {
		status = standards.StatusFalsePositive
	}
	var d engine.DecisionView
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/findings/"+url.PathEscape(pos[1])+"/accept", map[string]string{"status": status, "reason": *reason, "expires": *expires}, &d); err != nil {
		return err
	}
	return cl.print(d, *asJSON, func() {
		fmt.Fprintf(cl.out, "Recorded: %s until %s. It is in the audit trail with your reason, and it counts again after that date.\n", d.Status, time.UnixMilli(d.ExpiresAt).UTC().Format("2006-01-02"))
	})
}

func (cl *client) findingsReopen(args []string) error {
	fs := flag.NewFlagSet("findings reopen", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr("launchway findings reopen PROJECT FINGERPRINT")
	}
	if err := cl.call("POST", "/api/v1/projects/"+url.PathEscape(pos[0])+"/findings/"+url.PathEscape(pos[1])+"/reopen", map[string]string{}, nil); err != nil {
		return err
	}
	fmt.Fprintln(cl.out, "The decision is ended: the finding counts again.")
	return nil
}

func (cl *client) findingsExport(args []string) error {
	fs := flag.NewFlagSet("findings export", flag.ContinueOnError)
	format := fs.String("format", "json", "json, csv or sarif")
	status := fs.String("status", "", "open (default), fixed, accepted or all")
	outFile := fs.String("o", "", "write to this file instead of the screen")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway findings export PROJECT [--format json|csv|sarif] [--status S] [-o FILE]")
	}
	var got struct {
		Findings []engine.TrackedFinding `json:"findings"`
	}
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/findings/export?format=json&status="+url.QueryEscape(*status), nil, &got); err != nil {
		return err
	}
	w := cl.out
	if *outFile != "" {
		f, err := os.OpenFile(*outFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return usageErr("%v", err)
		}
		defer f.Close()
		w = f
	}
	var fs2 []standards.Finding
	for _, t := range got.Findings {
		fs2 = append(fs2, standards.Finding{Check: t.Check, Tool: t.Tool, Rule: t.Rule, Severity: findings.Severity(t.Severity), Kind: standards.Kind(t.Kind), Title: t.Title, File: t.File,
			Line: t.Line, Package: t.Package, Version: t.Version, FixedIn: t.FixedIn, Advisories: t.Advisories, Reachable: t.Reachable, Fingerprint: t.Fingerprint})
	}
	switch *format {
	case "csv":
		return standards.ExportCSV(w, fs2)
	case "sarif":
		return standards.ExportSARIF(w, fs2)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(got)
	}
	return usageErr("--format is json, csv or sarif")
}

func (cl *client) findingsInspect(args []string) error {
	fs := flag.NewFlagSet("findings inspect", flag.ContinueOnError)
	standing := fs.String("standing", "", "only new, existing, accepted or false-positive findings")
	asJSON := fs.Bool("json", false, "print as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("launchway findings inspect RUN [--standing S] [--json]")
	}
	var in engine.RunInspection
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/inspection?standing="+url.QueryEscape(*standing), nil, &in); err != nil {
		return err
	}
	return cl.print(in, *asJSON, func() {
		if !in.Present {
			fmt.Fprintln(cl.out, "This run has no Inspection (Standing orders were off or nothing applied when it was made).")
			return
		}
		verdict := "PASSED"
		if in.Blocked {
			verdict = "BLOCKED"
		}
		fmt.Fprintf(cl.out, "%s: %s\n", verdict, in.Summary)
		for _, c := range in.Checks {
			ran := "ran"
			if !c.Ran {
				ran = "did not run"
			}
			fmt.Fprintf(cl.out, "  %-16s %-7s %-12s %d findings, %d new\n", c.ID, c.State, ran, c.Total, c.New)
		}
		for _, f := range in.Findings {
			fmt.Fprintf(cl.out, "  [%s] %-8s %s %s:%d %s\n", f.Standing, f.Severity, f.Rule, f.File, f.Line, truncate(f.Title, 80))
		}
	})
}
