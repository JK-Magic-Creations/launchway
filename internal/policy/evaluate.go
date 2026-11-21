package policy

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Evaluation is the outcome of judging a run at one point: the verdict of
// every policy that applies, rule by rule, with what was found, what would
// satisfy the rule and the evidence; and everything it was judged from.
type Evaluation struct {
	Point       string         `json:"point"`
	Step        string         `json:"step,omitempty"`
	Action      string         `json:"action,omitempty"`
	Environment string         `json:"environment,omitempty"`
	At          int64          `json:"at"`
	Verdict     string         `json:"verdict"`
	Policies    []PolicyResult `json:"policies"`
	Input       Input          `json:"input"`
	// Waivers are the waivers for the run's project that were in the
	// database when it was judged (in force or not); the result says which
	// applied.
	Waivers []Waiver `json:"waivers,omitempty"`
}

// PolicyResult is one policy's verdict.
type PolicyResult struct {
	Policy  Policy       `json:"policy"` // as judged
	Verdict string       `json:"verdict"`
	Rules   []RuleResult `json:"rules"`
}

// RuleResult is one rule's verdict. Found says what was there, Want what
// would satisfy the rule, in words a person can act on.
type RuleResult struct {
	Rule     string        `json:"rule"`
	Verdict  string        `json:"verdict"`
	Found    string        `json:"found"`
	Want     string        `json:"want"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
	// Waiver is the waiver that let the rule pass; Waivers, for the
	// findings rule, the waivers of single findings that were counted.
	Waiver  *Waiver  `json:"waiver,omitempty"`
	Waivers []string `json:"waivers,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// EvidenceRef points at what a rule looked at, so the UI can link to it:
// a run's Tests, Security or Images tab, an approval step, a library.
type EvidenceRef struct {
	Kind      string `json:"kind"` // tests, findings, sbom, image, approval, step, library, deployment
	Step      string `json:"step,omitempty"`
	Run       string `json:"run,omitempty"`
	RunNumber int    `json:"run_number,omitempty"`
	Text      string `json:"text"`
}

// Blocked reports whether the evaluation stops what it was asked about.
func (e *Evaluation) Blocked() bool { return e.Verdict == VerdictBlock }

// Reason says, in one paragraph, why an evaluation blocks (or warns): each
// failing policy and rule, what was found and what would satisfy it.
func (e *Evaluation) Reason() string {
	var parts []string
	for _, p := range e.Policies {
		if p.Verdict != ResultFail {
			continue
		}
		for _, r := range p.Rules {
			if r.Verdict != ResultFail {
				continue
			}
			parts = append(parts, fmt.Sprintf("policy %s (%s), rule %s: %s; needs %s", p.Policy.Name, p.Policy.Mode, r.Rule, r.Found, r.Want))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	what := "blocked by policy"
	if e.Verdict != VerdictBlock {
		what = "policy warning"
	}
	return what + ": " + strings.Join(parts, ". ")
}

// Evaluate judges a run at one point. It is a pure function: the same
// policies, input, waivers and time always give the same evaluation.
func Evaluate(set Set, in Input, waivers []Waiver, at int64) Evaluation {
	ev := Evaluation{Point: in.Point, Step: in.Step, Action: in.Action, Environment: in.Environment, At: at, Verdict: VerdictPass, Input: in}
	ws := slices.Clone(waivers)
	sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })
	for _, w := range ws {
		if w.Project == in.Project {
			ev.Waivers = append(ev.Waivers, w)
		}
	}
	for _, src := range set.Sources {
		if !src.Covers(in.Project) {
			continue
		}
		if src.Error != "" {
			// Fail closed: nobody knows what the source would say.
			name := "source " + src.Name
			if src.Ref != "" {
				name += " at " + src.Ref
			}
			ev.Policies = append(ev.Policies, PolicyResult{
				Policy:  Policy{Name: src.Name, Mode: ModeEnforce, Source: src.Name, Version: src.Version, Description: "the policy source itself"},
				Verdict: ResultFail,
				Rules: []RuleResult{{Rule: RuleSource, Verdict: ResultFail,
					Found: fmt.Sprintf("the policies of %s could not be loaded: %s", name, src.Error),
					Want: "the source to load; until it does, every run of the projects it covers is refused at every policy point " +
						"(fix the source, or an administrator takes it out of the server's configuration); a waiver cannot pass this"}},
			})
			continue
		}
		for _, p := range src.Policies {
			if !p.AppliesTo(in.Point, in.Project, in.Branch, in.Event, in.Environment) {
				continue
			}
			ev.Policies = append(ev.Policies, judgePolicy(p, in, ev.Waivers, at))
		}
	}
	for _, p := range ev.Policies {
		if p.Verdict != ResultFail {
			continue
		}
		if p.Policy.Mode == ModeEnforce {
			ev.Verdict = VerdictBlock
		} else if ev.Verdict == VerdictPass {
			ev.Verdict = VerdictWarn
		}
	}
	return ev
}

// judgePolicy judges one policy's rules at the input's point.
func judgePolicy(p Policy, in Input, waivers []Waiver, at int64) PolicyResult {
	res := PolicyResult{Policy: p, Verdict: ResultPass}
	waived := false
	for _, rule := range p.Rules.used() {
		if PointOf(rule) != in.Point {
			continue
		}
		r := judge(p, rule, in, waivers, at)
		if r.Verdict == ResultFail {
			if w, notes := findWaiver(p, rule, in, waivers, at); w != nil {
				r.Verdict, r.Waiver = ResultWaived, w
				r.Notes = append(r.Notes, notes...)
			} else {
				r.Notes = append(r.Notes, notes...)
			}
		}
		switch r.Verdict {
		case ResultFail:
			res.Verdict = ResultFail
		case ResultWaived:
			waived = true
		}
		res.Rules = append(res.Rules, r)
	}
	if res.Verdict == ResultPass && waived {
		res.Verdict = ResultWaived
	}
	return res
}

// waiverApplies reports whether a waiver covers a rule of a policy for the
// input, and if not, why not (for a waiver that would otherwise match).
func waiverApplies(w *Waiver, p Policy, rule string, in Input, at int64) (bool, string) {
	if w.Project != in.Project || w.Policy != p.Name || w.Rule != rule || (w.Run != "" && w.Run != in.Run) ||
		(w.Environment != "" && w.Environment != in.Environment) {
		return false, ""
	}
	switch {
	case at < w.GrantedAt:
		return false, ""
	case at >= w.ExpiresAt:
		return false, fmt.Sprintf("waiver %s (%s) expired at %s", w.ID, w.GrantedBy, fmtTime(w.ExpiresAt))
	case in.Point == PointDeploy && in.Protected && !in.SelfApproval && in.Starter != "" && w.GrantedBy == in.Starter:
		return false, fmt.Sprintf("waiver %s was granted by %s, who started the run; environment %s is protected, so someone else must waive it",
			w.ID, w.GrantedBy, in.Environment)
	}
	return true, ""
}

// findWaiver finds a waiver of a whole rule (not of one finding).
func findWaiver(p Policy, rule string, in Input, waivers []Waiver, at int64) (*Waiver, []string) {
	var notes []string
	for i := range waivers {
		w := &waivers[i]
		if w.Finding != "" {
			continue
		}
		ok, why := waiverApplies(w, p, rule, in, at)
		if ok {
			c := *w
			return &c, notes
		}
		if why != "" {
			notes = append(notes, why)
		}
	}
	return nil, notes
}

func fmtTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC") }

// judge judges one rule. A rule's facts that are missing are never a pass.
func judge(p Policy, rule string, in Input, waivers []Waiver, at int64) RuleResult {
	r := RuleResult{Rule: rule, Verdict: ResultPass}
	fail := func(found, want string) RuleResult {
		r.Verdict, r.Found, r.Want = ResultFail, found, want
		return r
	}
	pass := func(found, want string) RuleResult {
		r.Found, r.Want = found, want
		return r
	}
	switch in.Point {
	case PointRun:
		if in.Pipeline == nil {
			return fail("nothing about the pipeline was recorded", "the pipeline's facts (this is a fault in Launchway; the rule fails closed)")
		}
	case PointDeploy:
		if in.Evidence == nil {
			return fail("no evidence of the run was recorded", "the run's evidence (this is a fault in Launchway; the rule fails closed)")
		}
	case PointImage:
		if in.Image == nil {
			return fail("nothing about the image step was recorded", "the step's settings (this is a fault in Launchway; the rule fails closed)")
		}
	}
	rs := p.Rules
	switch rule {
	case RuleLibraryVersions:
		want := "every library pinned by a tag or a full commit id"
		if rs.LibraryVersions.Pinned == "commit" {
			want = "every library pinned by a full commit id"
		}
		var bad []string
		for _, l := range in.Pipeline.Libraries {
			r.Evidence = append(r.Evidence, EvidenceRef{Kind: "library", Text: fmt.Sprintf("%s (%s) at %s, a %s, commit %s", l.Name, l.Repo, used(l.Version, l.Override), l.RefKind, short(l.Commit))})
			ok := l.RefKind == "commit" || (l.RefKind == "tag" && rs.LibraryVersions.Pinned == "tag")
			if !ok {
				bad = append(bad, fmt.Sprintf("%s at %s (a %s)", l.Name, used(l.Version, l.Override), nonEmptyStr(l.RefKind, "version of unknown kind")))
			}
		}
		if len(bad) > 0 {
			return fail("not pinned: "+strings.Join(bad, ", "), want)
		}
		if len(in.Pipeline.Libraries) == 0 {
			return pass("the pipeline uses no shared library", want)
		}
		return pass(fmt.Sprintf("%d libraries, all pinned", len(in.Pipeline.Libraries)), want)

	case RuleRequiredTemplates:
		var missing, wants []string
		for _, req := range rs.RequiredTemplates {
			w := req.Library + "/" + req.Template
			if len(req.Versions) > 0 {
				w += " at " + strings.Join(req.Versions, " or ") + " (a tag or a commit)"
			}
			wants = append(wants, w)
			found := false
			var seen []string
			for _, t := range in.Pipeline.Templates {
				if t.Library != req.Library || t.Template != req.Template {
					continue
				}
				seen = append(seen, fmt.Sprintf("%s at %s (a %s)", t.Group, t.Version, t.RefKind))
				if len(req.Versions) == 0 || (matchAny(req.Versions, t.Version) && t.RefKind != "branch") {
					found = true
					r.Evidence = append(r.Evidence, EvidenceRef{Kind: "step", Step: t.Group, Text: fmt.Sprintf("%s uses %s/%s at %s", t.Group, t.Library, t.Template, t.Version)})
				}
			}
			if !found {
				if len(seen) > 0 {
					missing = append(missing, fmt.Sprintf("%s/%s only at a version not allowed: %s", req.Library, req.Template, strings.Join(seen, ", ")))
				} else {
					missing = append(missing, fmt.Sprintf("no step uses %s/%s", req.Library, req.Template))
				}
			}
		}
		want := "the pipeline to use " + strings.Join(wants, "; ")
		if len(missing) > 0 {
			return fail(strings.Join(missing, "; "), want)
		}
		return pass("every required template is used", want)

	case RuleStepImages:
		si := rs.StepImages
		var parts []string
		if si.Digest {
			parts = append(parts, "pinned by digest (name@sha256:…)")
		}
		if len(si.Registries) > 0 {
			parts = append(parts, "from "+strings.Join(si.Registries, ", "))
		}
		want := "every step and container image " + strings.Join(parts, " and ")
		var bad []string
		for _, im := range in.Pipeline.Images {
			repo, digest := splitImage(im.Image)
			var why []string
			if si.Digest && !digestRe.MatchString(digest) {
				why = append(why, "not pinned by digest")
			}
			if len(si.Registries) > 0 && !matchRepo(si.Registries, repo) {
				why = append(why, "not from an allowed registry")
			}
			if len(why) > 0 {
				bad = append(bad, fmt.Sprintf("%s: %s (%s)", im.Where, im.Image, strings.Join(why, ", ")))
			}
		}
		if len(bad) > 0 {
			return fail(capList(bad), want)
		}
		return pass(fmt.Sprintf("%d images, all allowed", len(in.Pipeline.Images)), want)

	case RuleMaxTimeout:
		limit, _ := time.ParseDuration(rs.MaxTimeout)
		want := "no step longer than " + rs.MaxTimeout + " (approvals' waits are not counted)"
		var bad []string
		for _, t := range in.Pipeline.Timeouts {
			if time.Duration(t.Timeout)*time.Millisecond > limit {
				bad = append(bad, fmt.Sprintf("%s: %s", t.Step, (time.Duration(t.Timeout)*time.Millisecond).String()))
			}
		}
		if len(bad) > 0 {
			return fail("longer timeouts: "+capList(bad), want)
		}
		return pass("every timeout is within "+rs.MaxTimeout, want)

	case RuleForbidSecrets:
		want := "runs of " + strings.Join(rs.ForbidSecrets.Events, ", ") + " read no secrets"
		if !slices.Contains(rs.ForbidSecrets.Events, in.Event) {
			return pass("this is a "+in.Event+" run", want)
		}
		if !in.Pipeline.SecretsAllowed {
			return pass("this "+in.Event+" run may not read secrets (its project does not allow it)", want)
		}
		if len(in.Pipeline.Secrets) == 0 {
			return pass("no step asks for a secret", want)
		}
		var bad []string
		for _, s := range in.Pipeline.Secrets {
			bad = append(bad, s.Step+" ("+strings.Join(s.Names, ", ")+")")
		}
		return fail(fmt.Sprintf("this %s run may read secrets, and steps ask for them: %s", in.Event, capList(bad)),
			want+": turn secrets off for "+in.Event+" runs of the project, or take the secrets out of those steps")

	case RuleTests:
		return judgeTests(r, rs.Tests, in)
	case RuleFindings:
		return judgeFindings(r, p, rs.Findings, in, waivers, at)
	case RuleSBOM:
		want := fmt.Sprintf("a step before %s to make an SBOM of at least %d components", in.Step, rs.SBOM.MinComponents)
		best := -1
		for _, s := range in.Evidence.Steps {
			if s.SBOM != nil {
				r.Evidence = append(r.Evidence, EvidenceRef{Kind: "sbom", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
					Text: fmt.Sprintf("%s: an SBOM of %d components", s.Step, s.SBOM.Components)})
				best = max(best, s.SBOM.Components)
			}
		}
		switch {
		case best < 0:
			return fail("no step before "+in.Step+" made an SBOM", want)
		case best < rs.SBOM.MinComponents:
			return fail(fmt.Sprintf("the largest SBOM has %d components", best), want)
		}
		return pass(fmt.Sprintf("an SBOM of %d components", best), want)

	case RuleDeployedImages, RuleAllowedFailures:
		return judgeFacts(rule, r, rs, in)

	case RuleApprovals:
		want := fmt.Sprintf("%d approval(s) before %s by people other than the one who started the run", rs.Approvals.Min, in.Step)
		if in.Starter != "" {
			want = fmt.Sprintf("%d approval(s) before %s by people other than %s, who started the run", rs.Approvals.Min, in.Step, in.Starter)
		}
		var by []string
		self := false
		for _, s := range in.Evidence.Steps {
			if s.Approval == nil || s.Status != "succeeded" {
				continue
			}
			r.Evidence = append(r.Evidence, EvidenceRef{Kind: "approval", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
				Text: fmt.Sprintf("%s approved by %s", s.Step, nonEmptyStr(s.Approval.By, "nobody recorded"))})
			switch {
			case s.Approval.By == "":
			case s.Approval.By == in.Starter:
				self = true
			case !slices.Contains(by, s.Approval.By):
				by = append(by, s.Approval.By)
			}
		}
		sort.Strings(by)
		if len(by) < rs.Approvals.Min {
			found := "no approval before " + in.Step
			if len(by) > 0 {
				found = "approved by " + strings.Join(by, ", ") + " only"
			}
			if self {
				found += " (an approval by the person who started the run does not count)"
			}
			return fail(found, want)
		}
		return pass("approved by "+strings.Join(by, ", "), want)

	case RuleRefs:
		var wants []string
		if len(rs.Refs.Branches) > 0 {
			wants = append(wants, "a branch matching "+strings.Join(rs.Refs.Branches, ", "))
		}
		if len(rs.Refs.Tags) > 0 {
			wants = append(wants, "a tag matching "+strings.Join(rs.Refs.Tags, ", "))
		}
		want := "a run of " + strings.Join(wants, " or ")
		switch {
		case in.Branch != "" && matchAny(rs.Refs.Branches, in.Branch):
			return pass("branch "+in.Branch, want)
		case in.Tag != "" && matchAny(rs.Refs.Tags, in.Tag):
			return pass("tag "+in.Tag, want)
		case in.Branch != "":
			return fail("a run of branch "+in.Branch, want)
		case in.Tag != "":
			return fail("a run of tag "+in.Tag, want)
		}
		return fail("a "+in.Event+" run, of no branch or tag", want)

	case RuleImagePush:
		return judgeImagePush(r, rs.ImagePush, in)
	}
	return fail("rule "+rule+" is not known to this server", "a server that knows the rule")
}

func judgeTests(r RuleResult, t *Tests, in Input) RuleResult {
	want := fmt.Sprintf("at least %d test result(s) from the steps before %s, none failed", t.Min, in.Step)
	var total TestFact
	reported := 0
	var silent []string
	for _, s := range in.Evidence.Steps {
		if s.Tests == nil {
			if slices.Contains(s.Declared, "tests") && s.Status == "succeeded" {
				silent = append(silent, s.Step)
			}
			continue
		}
		reported++
		total.Total += s.Tests.Total
		total.Failed += s.Tests.Failed
		total.Errored += s.Tests.Errored
		total.Passed += s.Tests.Passed
		total.Skipped += s.Tests.Skipped
		r.Evidence = append(r.Evidence, EvidenceRef{Kind: "tests", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
			Text: fmt.Sprintf("%s: %d tests, %d passed, %d failed, %d errors, %d skipped", s.Step, s.Tests.Total, s.Tests.Passed, s.Tests.Failed, s.Tests.Errored, s.Tests.Skipped)})
	}
	found := fmt.Sprintf("%d tests from %d step(s): %d passed, %d failed, %d errors", total.Total, reported, total.Passed, total.Failed, total.Errored)
	if len(silent) > 0 {
		r.Notes = append(r.Notes, "these steps declare test reports but none were kept: "+strings.Join(silent, ", "))
	}
	switch {
	case reported == 0:
		r.Verdict, r.Found, r.Want = ResultFail, "no step before "+in.Step+" reported test results", want
	case total.Failed+total.Errored > 0:
		r.Verdict, r.Found, r.Want = ResultFail, found, want
	case total.Total < t.Min:
		r.Verdict, r.Found, r.Want = ResultFail, found, want
	default:
		r.Found, r.Want = found, want
	}
	return r
}

func judgeFindings(r RuleResult, p Policy, f *Findings, in Input, waivers []Waiver, at int64) RuleResult {
	var wants []string
	if f.MaxSeverity != "" {
		if f.MaxSeverity == "none" {
			wants = append(wants, "no findings at all")
		} else {
			wants = append(wants, "no finding worse than "+f.MaxSeverity)
		}
	}
	tools := sortedKeys(f.PerTool)
	for _, t := range tools {
		wants = append(wants, fmt.Sprintf("none from %s worse than %s", t, f.PerTool[t]))
	}
	if len(f.Scanners) > 0 {
		wants = append(wants, "reports from "+strings.Join(f.Scanners, ", "))
	}
	want := strings.Join(wants, "; ") + " (in the steps before " + in.Step + ")"
	var total Counts
	byTool := map[string]Counts{}
	ran := map[string]bool{}
	reported, byToolKnown := 0, true
	for _, s := range in.Evidence.Steps {
		if s.Findings == nil {
			continue
		}
		reported++
		total.add(s.Findings.Counts)
		for _, t := range s.Findings.Tools {
			ran[strings.ToLower(t)] = true
		}
		if s.Findings.ByTool == nil && len(s.Findings.Tools) > 0 {
			byToolKnown = false
		}
		for t, c := range s.Findings.ByTool {
			x := byTool[strings.ToLower(t)]
			x.add(c)
			byTool[strings.ToLower(t)] = x
		}
		r.Evidence = append(r.Evidence, EvidenceRef{Kind: "findings", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
			Text: fmt.Sprintf("%s: %s (%s)", s.Step, countText(s.Findings.Counts), nonEmptyStr(strings.Join(s.Findings.Tools, ", "), "no tool named"))})
	}
	if reported == 0 {
		r.Verdict, r.Found, r.Want = ResultFail, "no step before "+in.Step+" reported security findings: no scanner ran", want
		return r
	}
	var problems []string
	// Each threshold: how many findings break it, and which listed
	// findings would, so that waivers of single findings can be counted.
	type breach struct {
		what  string
		n     int
		match func(FindingFact) bool
	}
	var breaches []breach
	if f.MaxSeverity != "" {
		th := f.MaxSeverity
		if n := total.Above(th); n > 0 {
			breaches = append(breaches, breach{fmt.Sprintf("%d worse than %s", n, th), n,
				func(x FindingFact) bool { return th == "none" || rank(x.Severity) > rank(th) }})
		}
	}
	for _, t := range tools {
		th := f.PerTool[t]
		lt := strings.ToLower(t)
		if !byToolKnown {
			problems = append(problems, "the findings of an earlier step were kept without counts per tool, so the counts for "+t+" cannot be shown")
			continue
		}
		if n := byTool[lt].Above(th); n > 0 {
			breaches = append(breaches, breach{fmt.Sprintf("%d from %s worse than %s", n, t, th), n,
				func(x FindingFact) bool {
					return strings.EqualFold(x.Tool, t) && (th == "none" || rank(x.Severity) > rank(th))
				}})
		}
	}
	for _, s := range f.Scanners {
		if !ran[strings.ToLower(s)] {
			problems = append(problems, s+" did not report")
		}
	}
	// Waivers of single findings: a breach passes when every finding that
	// makes it is listed and waived.
	var used []string
	for _, b := range breaches {
		waived := 0
		for _, x := range in.Evidence.Findings {
			if !b.match(x) {
				continue
			}
			for i := range waivers {
				w := &waivers[i]
				if w.Finding != x.Fingerprint {
					continue
				}
				if ok, why := waiverApplies(w, p, RuleFindings, in, at); ok {
					waived++
					if !slices.Contains(used, w.ID) {
						used = append(used, w.ID)
					}
					break
				} else if why != "" && !slices.Contains(r.Notes, why) {
					r.Notes = append(r.Notes, why)
				}
			}
		}
		if left := b.n - waived; left > 0 {
			msg := b.what
			if waived > 0 {
				msg += fmt.Sprintf(" (%d of them waived)", waived)
			}
			problems = append(problems, msg)
		}
	}
	r.Waivers = used
	found := fmt.Sprintf("%s from %s", countText(total), strings.Join(sortedTrue(ran), ", "))
	if len(problems) > 0 {
		r.Verdict, r.Found, r.Want = ResultFail, strings.Join(problems, "; ")+"; in all "+found, want
		if !in.Evidence.FindingsListed && len(breaches) > 0 {
			r.Notes = append(r.Notes, "not every finding could be listed, so waivers of single findings may not cover them all")
		}
		return r
	}
	if len(used) > 0 {
		r.Verdict = ResultWaived
		found += "; waived findings: " + strings.Join(used, ", ")
	}
	r.Found, r.Want = found, want
	return r
}

func judgeDeployed(r RuleResult, d *DeployedImages, in Input) RuleResult {
	var wants []string
	if d.Digest {
		wants = append(wants, "pinned by digest")
	}
	if d.BuiltByRun {
		wants = append(wants, "built by an image step of this run")
	}
	if d.Signed {
		wants = append(wants, "signed")
	}
	if d.Scanned {
		wants = append(wants, "scanned (and not failed by its scan)")
	}
	want := "every image the deployment writes " + strings.Join(wants, ", ")
	ev := in.Evidence
	if len(ev.Deployed) == 0 {
		r.Verdict, r.Found, r.Want = ResultFail, fmt.Sprintf("the deployment writes no image reference (%d change(s), none naming an image with tag or digest)", ev.Changes),
			want+"; write the image with a change that names it (image: and digest:)"
		return r
	}
	built := func(digest string) (*BuiltImage, StepFact) {
		for _, s := range ev.Steps {
			if s.Image == nil {
				continue
			}
			if s.Image.Digest == digest || slices.Contains(s.Image.Platforms, digest) {
				return s.Image, s
			}
		}
		return nil, StepFact{}
	}
	var bad []string
	for _, ref := range ev.Deployed {
		name := ref.Image
		if ref.Digest != "" {
			name += "@" + ref.Digest
		} else {
			name += ":" + ref.Tag
		}
		var why []string
		if d.Digest && ref.Digest == "" {
			why = append(why, "pinned by tag, not digest")
		}
		var img *BuiltImage
		if ref.Digest != "" {
			var s StepFact
			if img, s = built(ref.Digest); img != nil {
				r.Evidence = append(r.Evidence, EvidenceRef{Kind: "image", Step: s.Step, Run: s.Run, RunNumber: s.RunNumber,
					Text: fmt.Sprintf("%s built %s (signed: %t, scanned: %t)", s.Step, short(img.Digest), img.Signed, img.Scanned && !img.ScanFailed)})
			}
		}
		if (d.BuiltByRun || d.Signed || d.Scanned) && img == nil {
			if ref.Digest == "" {
				why = append(why, "named by a tag, so which image it is cannot be shown")
			} else {
				why = append(why, "no image step before "+in.Step+" built this digest")
			}
		}
		if img != nil {
			if d.Signed && !img.Signed {
				why = append(why, "not signed")
			}
			if d.Scanned && (!img.Scanned || img.ScanFailed) {
				if img.ScanFailed {
					why = append(why, "its scan failed it")
				} else {
					why = append(why, "not scanned")
				}
			}
		}
		if len(why) > 0 {
			bad = append(bad, fmt.Sprintf("%s in %s: %s", name, ref.File, strings.Join(why, ", ")))
		}
	}
	if len(bad) > 0 {
		r.Verdict, r.Found, r.Want = ResultFail, capList(bad), want
		return r
	}
	r.Found, r.Want = fmt.Sprintf("%d image reference(s), all as required", len(ev.Deployed)), want
	return r
}

func judgeImagePush(r RuleResult, ip *ImagePush, in Input) RuleResult {
	var wants []string
	if ip.Scan != "" {
		wants = append(wants, "scan: { mode: enforce, fail_on: "+ip.Scan+" or stricter }")
	}
	switch ip.Sign {
	case "runner":
		wants = append(wants, "sign: true (the runner's key)")
	case "any":
		wants = append(wants, "sign")
	}
	if ip.SBOM {
		wants = append(wants, "sbom: true")
	}
	if ip.Provenance {
		wants = append(wants, "provenance: true")
	}
	if len(ip.Registries) > 0 {
		wants = append(wants, "push only to "+strings.Join(ip.Registries, ", "))
	}
	want := "an image step that pushes to have " + strings.Join(wants, ", ")
	im := in.Image
	if !im.Push {
		r.Found, r.Want = "the step pushes nothing", want
		return r
	}
	var bad []string
	if ip.Scan != "" {
		switch {
		case im.ScanMode == "":
			bad = append(bad, "it does not scan")
		case im.ScanMode != "enforce":
			bad = append(bad, "its scan only reports (mode "+im.ScanMode+")")
		case rank(im.ScanFailOn) < 1 || rank(im.ScanFailOn) > rank(ip.Scan):
			bad = append(bad, "its scan fails only on "+nonEmptyStr(im.ScanFailOn, "nothing"))
		}
	}
	switch {
	case ip.Sign != "" && im.Sign == "":
		bad = append(bad, "it does not sign")
	case ip.Sign == "runner" && im.Sign != "runner":
		bad = append(bad, "it signs with a key from a secret store, not the runner's own key")
	}
	if ip.SBOM && !im.SBOM {
		bad = append(bad, "it makes no SBOM")
	}
	if ip.Provenance && !im.Provenance {
		bad = append(bad, "it records no provenance")
	}
	for _, reg := range append(slices.Clone(im.Registries), nonEmptyList(im.Cache)...) {
		if len(ip.Registries) > 0 && !matchAny(ip.Registries, reg) {
			bad = append(bad, "it pushes to "+reg)
		}
	}
	if len(bad) > 0 {
		r.Verdict, r.Found, r.Want = ResultFail, strings.Join(bad, "; "), want
		return r
	}
	r.Found, r.Want = "it pushes with every safeguard asked for", want
	return r
}

// ---- helpers ----

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// splitImage splits an image reference into its repository (with a
// registry host; docker.io/library/ for the short names Docker accepts)
// and its digest, if pinned.
func splitImage(ref string) (repo, digest string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	// Drop a tag: a colon after the last slash.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	first, _, hasSlash := strings.Cut(ref, "/")
	if !hasSlash || !(strings.ContainsAny(first, ".:") || first == "localhost") {
		if !hasSlash {
			ref = "library/" + ref
		}
		ref = "docker.io/" + ref
	}
	return ref, digest
}

// RepositoryOf is an image reference's repository, with its registry host.
func RepositoryOf(ref string) string { r, _ := splitImage(ref); return r }

// matchRepo matches a repository against patterns: * within a part, a
// final /** for any depth below.
func matchRepo(patterns []string, repo string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "/**"); ok {
			parts := strings.Split(repo, "/")
			n := len(strings.Split(prefix, "/"))
			if len(parts) > n {
				if ok, _ := matchPath(prefix, strings.Join(parts[:n], "/")); ok {
					return true
				}
			}
			continue
		}
		if ok, _ := matchPath(p, repo); ok {
			return true
		}
	}
	return false
}

func matchPath(p, s string) (bool, error) {
	if !strings.Contains(p, "*") && !strings.Contains(p, "?") && !strings.Contains(p, "[") {
		return p == s, nil
	}
	return path.Match(p, s)
}

func used(version, override string) string {
	if override != "" {
		return override + " (overriding " + version + ")"
	}
	return version
}

func short(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func nonEmptyStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonEmptyList(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// capList joins at most 10 items, saying how many more there were.
func capList(items []string) string {
	if len(items) <= 10 {
		return strings.Join(items, "; ")
	}
	return strings.Join(items[:10], "; ") + fmt.Sprintf("; and %d more", len(items)-10)
}

func countText(c Counts) string {
	return fmt.Sprintf("%d findings (%d critical, %d high, %d medium, %d low, %d unknown)", c.Total, c.Critical, c.High, c.Medium, c.Low, c.Unknown)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTrue(m map[string]bool) []string {
	out := sortedKeys(m)
	if len(out) == 0 {
		return []string{"no tool named"}
	}
	return out
}

// FindingFloor is the most lenient severity a findings rule of these
// policies fails on, so the engine lists every finding a waiver might need
// to name: findings worse than it. "" when no rule judges findings.
func FindingFloor(ps []Policy) string {
	floor := ""
	consider := func(th string) {
		if th != "" && (floor == "" || rank(th) < rank(floor)) {
			floor = th
		}
	}
	for _, p := range ps {
		if f := p.Rules.Findings; f != nil {
			consider(f.MaxSeverity)
			for _, th := range f.PerTool {
				consider(th)
			}
		}
	}
	return floor
}

// Worse reports whether a severity is worse than a threshold.
func Worse(severity, threshold string) bool {
	return threshold == "none" || rank(severity) > rank(threshold)
}
