package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// Proposal is one pull request to open or keep: a group of updates.
type Proposal struct {
	Key       string   `json:"key"`    // stable: the same updates keep the same key between runs
	Branch    string   `json:"branch"` // where the change is pushed
	Title     string   `json:"title"`
	Group     string   `json:"group"` // the settings group, or "" for a single dependency
	Ecosystem string   `json:"ecosystem"`
	Directory string   `json:"directory"`
	Security  bool     `json:"security"`
	Updates   []Update `json:"updates"`
}

// Action of a decision.
const (
	ActionOpen   = "open"   // a new pull request
	ActionUpdate = "update" // the open one changes in place
	ActionKeep   = "keep"   // the open one is still right
	ActionQueue  = "queued" // would open, but a limit says wait
	ActionClose  = "close"  // the open one is no longer needed
)

// Decision says what the updater does with one proposal.
type Decision struct {
	Proposal
	Action string `json:"action"`
	Why    string `json:"why"`
	Number int    `json:"number,omitempty"` // the existing pull request's number
}

// Open describes an update pull request that is open now.
type Open struct {
	Key     string
	Number  int
	Targets map[string]string // dependency name to the version it moves to
}

// Input of Plan.
type Input struct {
	Updates   []Update
	Policy    Policy
	Open      []Open
	Now       time.Time
	LastRun   time.Time // when routine updates were last looked for; zero: never
	Immediate bool      // ignore the schedule (a person asked to run now)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// Plan groups the proposed updates and applies the schedule and the limits.
// Order of opening: security fixes (most severe first), then patch, minor,
// major. It returns one decision per proposal and one "close" per open pull
// request that no longer has a matching proposal.
func Plan(in Input) []Decision {
	p := in.Policy
	proposals := group(in.Updates, p)
	open := map[string]Open{}
	for _, o := range in.Open {
		open[o.Key] = o
	}
	routineDue := in.Immediate || Due(in.Now, p.Schedule, in.LastRun)
	enabled := func(e string) bool { return p.Enabled && p.Ecosystems[e].Enabled }

	sort.SliceStable(proposals, func(i, j int) bool { return rank(proposals[i]) < rank(proposals[j]) })

	openCount := len(in.Open)
	perEco := map[string]int{}
	for _, o := range in.Open {
		perEco[ecoOfKey(o.Key)]++
	}
	opened := 0
	var out []Decision
	seen := map[string]bool{}
	for _, pr := range proposals {
		seen[pr.Key] = true
		d := Decision{Proposal: pr}
		ex, isOpen := open[pr.Key]
		d.Number = ex.Number
		switch {
		case !enabled(pr.Ecosystem):
			d.Action, d.Why = ActionQueue, "Updates are switched off for "+pr.Ecosystem+"."
		case pr.Security && !p.Security:
			d.Action, d.Why = ActionQueue, "Security updates are switched off."
		case !pr.Security && !p.Routine:
			d.Action, d.Why = ActionQueue, "Routine updates are switched off."
		case isOpen && sameTargets(ex.Targets, pr):
			d.Action, d.Why = ActionKeep, "The open pull request already proposes exactly this."
		case isOpen:
			d.Action, d.Why = ActionUpdate, "A newer version appeared or the set changed; the pull request is updated in place."
		case !pr.Security && !routineDue:
			d.Action, d.Why = ActionQueue, "Routine updates wait for the schedule ("+Describe(p.Schedule)+")."
		case opened >= p.Limits.MaxPerRun:
			d.Action, d.Why = ActionQueue, fmt.Sprintf("This run already opened %d pull requests (limit %d); the rest wait for the next run.", opened, p.Limits.MaxPerRun)
		case !pr.Security && openCount >= p.Limits.MaxOpen:
			d.Action, d.Why = ActionQueue, fmt.Sprintf("%d update pull requests are open (limit %d); this waits until one is merged or closed.", openCount, p.Limits.MaxOpen)
		case !pr.Security && p.Limits.MaxOpenPerEcosystem > 0 && perEco[pr.Ecosystem] >= p.Limits.MaxOpenPerEcosystem:
			d.Action, d.Why = ActionQueue, fmt.Sprintf("%d %s update pull requests are open (limit %d per ecosystem).", perEco[pr.Ecosystem], pr.Ecosystem, p.Limits.MaxOpenPerEcosystem)
		default:
			d.Action, d.Why = ActionOpen, openWhy(pr)
			opened++
			openCount++
			perEco[pr.Ecosystem]++
		}
		out = append(out, d)
	}
	for _, o := range in.Open {
		if !seen[o.Key] {
			out = append(out, Decision{Proposal: Proposal{Key: o.Key, Ecosystem: ecoOfKey(o.Key), Branch: branch(p, o.Key)}, Action: ActionClose, Why: "Nothing needs updating here any more: it was merged, changed by hand, or the dependency was removed.", Number: o.Number})
		}
	}
	return out
}

func openWhy(pr Proposal) string {
	if pr.Security {
		return "A security fix: opened at once, outside the schedule."
	}
	return "A routine update within the limits."
}

func ecoOfKey(k string) string { return strings.SplitN(k, "-", 2)[0] }

func rank(p Proposal) int {
	r := 4
	if p.Security {
		r = 0
	}
	worst := version.KindPatch
	for _, u := range p.Updates {
		if u.Kind == version.KindMajor {
			worst = version.KindMajor
		} else if u.Kind == version.KindMinor && worst == version.KindPatch {
			worst = version.KindMinor
		}
	}
	switch worst {
	case version.KindPatch:
		return r + 1
	case version.KindMinor:
		return r + 2
	}
	return r + 3
}

func sameTargets(old map[string]string, pr Proposal) bool {
	if len(old) != len(pr.Updates) {
		return false
	}
	for _, u := range pr.Updates {
		if old[u.Dep.Name] != u.Target {
			return false
		}
	}
	return true
}

func branch(p Policy, key string) string {
	pre := p.Commit.BranchPrefix
	if pre == "" {
		pre = DefaultBranchPrefix
	}
	return strings.TrimRight(pre, "/") + "/" + key
}

func group(updates []Update, p Policy) []Proposal {
	byKey := map[string]*Proposal{}
	var order []string
	add := func(key, title, grp string, u Update) {
		pr := byKey[key]
		if pr == nil {
			pr = &Proposal{Key: key, Branch: branch(p, key), Title: title, Group: grp, Ecosystem: u.Dep.Ecosystem, Directory: depupdate.Dir(u.Dep.File)}
			byKey[key] = pr
			order = append(order, key)
		}
		pr.Updates = append(pr.Updates, u)
		pr.Security = pr.Security || u.Security
	}
	for _, u := range updates {
		if u.Major != nil && u.Status != StatusSecurity {
			mu := u
			mu.Target, mu.Kind, mu.Published, mu.NotesURL, mu.Digest = u.Major.Target, version.KindMajor, u.Major.Published, u.Major.NotesURL, ""
			mu.Major = nil
			d := mu.Dep
			add(d.Ecosystem+"-"+slug(d.Name)+"-major"+dirSlug(depupdate.Dir(d.File)), "", "", mu)
		}
		if u.Target == "" || (u.Status != StatusUpdate && u.Status != StatusSecurity) {
			continue
		}
		d := u.Dep
		dir := depupdate.Dir(d.File)
		if u.Security {
			add(d.Ecosystem+"-security-"+slug(d.Name)+dirSlug(dir), "Fix "+advisoryList2(u)+" by updating "+shortName(d.Name)+" to "+u.Target, "", u)
			continue
		}
		if g, ok := matchGroup(p.Groups, u); ok {
			add(d.Ecosystem+"-"+slug(g.Name)+dirSlug(dir), titleFor(p, g.Name, d.Ecosystem, dir), g.Name, u)
			continue
		}
		add(d.Ecosystem+"-"+slug(d.Name)+dirSlug(dir), "", "", u)
	}
	var out []Proposal
	for _, k := range order {
		pr := byKey[k]
		sort.Slice(pr.Updates, func(i, j int) bool { return pr.Updates[i].Dep.Name < pr.Updates[j].Dep.Name })
		if pr.Title == "" {
			u := pr.Updates[0]
			pr.Title = fmt.Sprintf("%s %s from %s to %s", commitPrefix(p), shortName(u.Dep.Name), u.Dep.Current, u.Target)
		}
		out = append(out, *pr)
	}
	return out
}

func advisoryList2(u Update) string {
	if len(u.Advisories) == 0 {
		return "a vulnerability"
	}
	return u.Advisories[0].ID
}

func commitPrefix(p Policy) string {
	if p.Commit.MessagePrefix != "" {
		return p.Commit.MessagePrefix
	}
	return DefaultCommitPrefix
}

func titleFor(p Policy, group, eco, dir string) string {
	t := fmt.Sprintf("%s %s dependencies (%s)", commitPrefix(p), eco, strings.ReplaceAll(group, "-", " "))
	if dir != "" {
		t += " in " + dir
	}
	return t
}

func dirSlug(dir string) string {
	if dir == "" {
		return ""
	}
	return "-" + slug(dir)
}

func shortName(n string) string {
	if i := strings.Index(n, "docker.io/library/"); i == 0 {
		return strings.TrimPrefix(n, "docker.io/library/")
	}
	return n
}

// matchGroup finds the first group that takes an update: ecosystem, name
// patterns, directory and kinds (default patch and minor) must all fit. A
// major update is only grouped when the group lists "major".
func matchGroup(groups []Group, u Update) (Group, bool) {
	for _, g := range groups {
		if g.Ecosystem != "" && g.Ecosystem != u.Dep.Ecosystem {
			continue
		}
		if len(g.Patterns) > 0 && !AnyGlob(g.Patterns, u.Dep.Name) {
			continue
		}
		if g.Directory != "" && g.Directory != depupdate.Dir(u.Dep.File) {
			continue
		}
		kinds := g.Kinds
		if len(kinds) == 0 {
			kinds = []string{"patch", "minor"}
		}
		k := string(u.Kind)
		if u.Kind == version.KindOther {
			k = "patch"
		}
		if contains(kinds, k) {
			return g, true
		}
	}
	return Group{}, false
}
