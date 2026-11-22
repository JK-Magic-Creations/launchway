package standards

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// Source says where a value comes from.
type Source struct {
	Kind  string `json:"kind"`            // default, level or file
	Level string `json:"level,omitempty"` // installation, organization, unit, project, file
	Scope string `json:"scope,omitempty"` // the unit's, project's or organization's name
}

// LockInfo says a value is locked at a level, and why.
type LockInfo struct {
	Level  string `json:"level"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason"`
	// Who can change it, in words.
	Who string `json:"who"`
}

// Link is one value a setting has in the chain, nearest first.
type Link struct {
	Source
	Display string `json:"display"`
	// Overridden: a nearer level set another value.
	Overridden bool `json:"overridden,omitempty"`
	// Ignored says why the value does not count: a lock above forbids it.
	Ignored string `json:"ignored,omitempty"`
}

// Value is a setting in effect: the winner, where it comes from, what it
// overrides, and whether a lock holds it.
type Value[T any] struct {
	V      T         `json:"value"`
	From   Source    `json:"from"`
	Locked *LockInfo `json:"locked,omitempty"`
	Chain  []Link    `json:"chain,omitempty"`
}

// LevelDoc is the settings of one level, for one scope.
type LevelDoc struct {
	Level    string   `json:"level"`
	Scope    string   `json:"scope,omitempty"`
	Settings Settings `json:"settings"`
}

// EffectiveCheck is a check as it applies to one repository.
type EffectiveCheck struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Why       string       `json:"why"`
	Kind      Kind         `json:"kind"`
	Tool      string       `json:"tool"`
	Ecosystem string       `json:"ecosystem"`
	Gates     bool         `json:"gates"`
	Network   string       `json:"network,omitempty"`
	Fix       string       `json:"fix"`
	Report    Report       `json:"report"`
	Budget    int          `json:"budget_seconds"`
	State     Value[State] `json:"state"`
	Applies   bool         `json:"applies"`
	Because   string       `json:"because"`
	Active    bool         `json:"active"` // it will run: it applies, it is available, it is not off and Standing orders are not off
	Reason    string       `json:"reason"` // when not active: why, in a sentence
	Unavail   string       `json:"unavailable,omitempty"`
	License   string       `json:"license,omitempty"`
	Image     string       `json:"image,omitempty"`
}

// ExemptionInfo is an exemption in effect, with where it came from.
type ExemptionInfo struct {
	Ignore
	From    Source `json:"from"`
	Expired bool   `json:"expired,omitempty"`
}

// Effective is the profile that applies to one repository.
type Effective struct {
	ProfileVersion string                   `json:"profile_version"`
	Off            Value[bool]              `json:"off"`
	OffReason      string                   `json:"off_reason,omitempty"`
	Checks         []EffectiveCheck         `json:"checks"`
	FailOn         Value[findings.Severity] `json:"fail_on"`
	RequireFix     Value[bool]              `json:"require_fix"`
	DefaultBranch  Value[State]             `json:"default_branch"`
	Incomplete     Value[string]            `json:"incomplete"`
	Allow          Value[[]string]          `json:"licenses_allow"`
	Deny           Value[[]string]          `json:"licenses_deny"`
	Exemptions     []ExemptionInfo          `json:"exemptions"`
	Detected       []Detected               `json:"detected"`
	// Notices are things the team should know, each one plain sentence: a
	// value that did not count because of a lock, a problem in the file.
	Notices []string `json:"notices"`
}

// ResolveInput is what an effective profile is made of.
type ResolveInput struct {
	// Levels are the documents of the levels that apply to the repository,
	// widest first: Base, Wing, Squadron, Project. Missing levels are
	// omitted.
	Levels []LevelDoc
	// File is the repository's file, read from the default branch; nil when
	// there is none. FileProblems are what ParseFile said.
	File         *Settings
	FileProblems []string
	// Files are the repository's paths, for detecting what applies.
	Files []string
	Now   time.Time
	// Words are the names of the levels in messages; DefaultWords if nil.
	Words map[string]string
}

func (in ResolveInput) word(level string) string {
	if w := in.Words[level]; w != "" {
		return w
	}
	return DefaultWords[level]
}

// layer is one document in the order of resolution.
type layer struct {
	src    Source
	set    Settings
	isFile bool
}

func (in ResolveInput) layers() []layer {
	var ls []layer
	for _, l := range in.Levels {
		ls = append(ls, layer{src: Source{Kind: "level", Level: l.Level, Scope: l.Scope}, set: l.Settings})
	}
	if in.File != nil {
		ls = append(ls, layer{src: Source{Kind: "file", Level: LevelFile}, set: *in.File, isFile: true})
	}
	return ls
}

// who says who can change a locked value.
func (in ResolveInput) who(src Source) string {
	switch src.Level {
	case LevelInstallation:
		return "an administrator of the " + in.word(LevelInstallation)
	case LevelOrganization:
		return "an administrator of your " + in.word(LevelOrganization)
	case LevelUnit:
		return "an administrator of the " + in.word(LevelUnit) + " " + src.Scope
	}
	return "an administrator of the project"
}

func (in ResolveInput) where(src Source) string {
	switch {
	case src.Kind == "default":
		return "the default"
	case src.Level == LevelFile:
		return "the repository file " + FilePath
	case src.Scope != "" && src.Level != LevelOrganization:
		return "the " + in.word(src.Level) + " " + src.Scope
	}
	return "the " + in.word(src.Level)
}

// pick resolves one setting through the layers. get returns the value a
// layer sets (and whether it sets one); weaker says whether proposed is
// weaker than the floor a lock set. show writes a value for messages.
func pick[T any](in ResolveInput, ls []layer, key string, def T, get func(Settings) (T, bool), weaker func(proposed, floor T) bool, show func(T) string, notices *[]string) Value[T] {
	cur := Value[T]{V: def, From: Source{Kind: "default"}}
	chain := []Link{{Source: cur.From, Display: show(def)}}
	var floor T
	var lock *LockInfo
	for _, l := range ls {
		if v, ok := get(l.set); ok {
			if lock != nil && weaker(v, floor) {
				msg := fmt.Sprintf("locked by %s", in.where(Source{Kind: "level", Level: lock.Level, Scope: lock.Scope}))
				chain = append(chain, Link{Source: l.src, Display: show(v), Ignored: msg})
				*notices = append(*notices, fmt.Sprintf("%s sets %s to %s, but it is %s (%s), so %s still applies.",
					capital(in.where(l.src)), KeyName(key), show(v), msg, lock.Reason, show(cur.V)))
			} else {
				cur.V, cur.From = v, l.src
				chain = append(chain, Link{Source: l.src, Display: show(v)})
			}
		}
		if reason, ok := l.set.Locks[key]; ok && !l.isFile {
			floor = cur.V
			lock = &LockInfo{Level: l.src.Level, Scope: l.src.Scope, Reason: reason, Who: in.who(l.src)}
		}
	}
	cur.Locked = lock
	// The chain, nearest first, with what each value was overridden by.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	won := false
	for i := range chain {
		if chain[i].Ignored == "" && !won {
			won = true
			continue
		}
		if chain[i].Ignored == "" {
			chain[i].Overridden = true
		}
	}
	if len(chain) > 1 {
		cur.Chain = chain
	}
	return cur
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func showBool(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func showList(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

func sevRank(s findings.Severity) int {
	switch s {
	case findings.Critical:
		return 4
	case findings.High:
		return 3
	case findings.Medium:
		return 2
	case findings.Low:
		return 1
	}
	return 0
}

// subset reports whether every name of a is in b (case-insensitively).
func subset(a, b []string) bool {
	in := map[string]bool{}
	for _, x := range b {
		in[strings.ToLower(x)] = true
	}
	for _, x := range a {
		if !in[strings.ToLower(x)] {
			return false
		}
	}
	return true
}

// Resolve makes the effective profile of a repository. It is a pure
// function of its input: the same levels, file and files always give the
// same answer, which is what lets a run keep the profile it was made under
// and explain its verdict later.
func Resolve(in ResolveInput) Effective {
	ls := in.layers()
	var notices []string
	e := Effective{ProfileVersion: ProfileVersion, Detected: Detect(in.Files), Exemptions: []ExemptionInfo{}}
	e.Off = pick(in, ls, KeyOff, false, func(s Settings) (bool, bool) { return derefBool(s.Off) },
		func(proposed, floor bool) bool { return proposed != floor }, showBool, &notices)
	for _, l := range ls {
		if l.set.Off != nil && *l.set.Off && l.set.OffReason != "" && l.src == e.Off.From {
			e.OffReason = l.set.OffReason
		}
	}
	e.FailOn = pick(in, ls, KeyFailOn, Defaults.FailOn, func(s Settings) (findings.Severity, bool) { return s.Gate.FailOn, s.Gate.FailOn != "" },
		func(p, f findings.Severity) bool { return sevRank(p) > sevRank(f) }, func(v findings.Severity) string { return string(v) }, &notices)
	e.RequireFix = pick(in, ls, KeyRequireFix, Defaults.RequireFix, func(s Settings) (bool, bool) { return derefBool(s.Gate.RequireFix) },
		func(p, f bool) bool { return p && !f }, showBool, &notices)
	e.DefaultBranch = pick(in, ls, KeyDefaultBranch, Defaults.DefaultBranch, func(s Settings) (State, bool) { return s.Gate.DefaultBranch, s.Gate.DefaultBranch != "" },
		func(p, f State) bool { return p.Rank() < f.Rank() }, func(v State) string { return string(v) }, &notices)
	e.Incomplete = pick(in, ls, KeyIncomplete, Defaults.Incomplete, func(s Settings) (string, bool) { return s.Gate.Incomplete, s.Gate.Incomplete != "" },
		func(p, f string) bool { return p == IncompletePass && f == IncompleteFail }, func(v string) string { return v }, &notices)
	e.Deny = pick(in, ls, KeyDeny, append([]string(nil), Defaults.Deny...), func(s Settings) ([]string, bool) { return derefList(s.Licenses.Deny) },
		func(p, f []string) bool { return !subset(f, p) }, showList, &notices)
	e.Allow = pick(in, ls, KeyAllow, []string{}, func(s Settings) ([]string, bool) { return derefList(s.Licenses.Allow) },
		func(p, f []string) bool { return len(f) > 0 && (len(p) == 0 || !subset(p, f)) }, showList, &notices)

	// Exemptions: each level adds its own; a lock on them at a level drops
	// those of the levels below, the file included.
	locked := false
	for _, l := range ls {
		if locked && len(l.set.Ignore) > 0 {
			notices = append(notices, fmt.Sprintf("%s lists %d exemption(s), but exemptions are locked above (%s), so they do not count.",
				capital(in.where(l.src)), len(l.set.Ignore), "an administrator of a higher level"))
		} else {
			for _, ig := range l.set.Ignore {
				if ig.Status == "" {
					ig.Status = StatusAccepted
				}
				e.Exemptions = append(e.Exemptions, ExemptionInfo{Ignore: ig, From: l.src, Expired: ig.Expired(in.Now)})
			}
		}
		if _, ok := l.set.Locks[KeyIgnore]; ok && !l.isFile {
			locked = true
		}
	}

	// Checks.
	for _, c := range Checks() {
		key := CheckKey(c.ID)
		st := pick(in, ls, key, c.Default, func(s Settings) (State, bool) { v, ok := s.Checks[c.ID]; return v, ok },
			func(p, f State) bool { return p.Rank() < f.Rank() }, func(v State) string { return string(v) }, &notices)
		if st.V == Block && !c.Gates {
			st.V = Advise
		}
		because, applies := c.Applies(in.Files)
		ec := EffectiveCheck{ID: c.ID, Title: c.Title, Why: c.Why, Kind: c.Kind, Tool: c.Tool, Ecosystem: c.Ecosystem, Gates: c.Gates, Network: c.Network, Fix: c.Fix,
			Report: c.Report, Budget: int(c.Budget / time.Second), State: st, Applies: applies, Because: because, Unavail: c.Unavailable}
		if t, ok := ToolNamed(c.Tool); ok {
			ec.License, ec.Image = t.License, t.Image
		}
		switch {
		case !c.Available():
			ec.Reason = "not available yet: " + c.Unavailable
		case e.Off.V:
			ec.Reason = "Standing orders are switched off"
		case !applies:
			ec.Reason = "nothing in the repository makes it apply"
		case st.V == Off:
			ec.Reason = "switched off by " + in.where(st.From)
		default:
			ec.Active = true
		}
		e.Checks = append(e.Checks, ec)
	}
	if in.File != nil {
		for _, p := range in.FileProblems {
			notices = append(notices, FilePath+": "+p)
		}
	}
	sort.Strings(notices)
	e.Notices = dedupe(notices)
	return e
}

func derefBool(p *bool) (bool, bool) {
	if p == nil {
		return false, false
	}
	return *p, true
}

func derefList(p *[]string) ([]string, bool) {
	if p == nil {
		return nil, false
	}
	return append([]string{}, *p...), true
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}
