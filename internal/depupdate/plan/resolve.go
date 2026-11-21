package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// Status of one dependency after resolution.
const (
	StatusUpToDate   = "up-to-date"
	StatusUpdate     = "update"     // a version is proposed
	StatusSecurity   = "security"   // a version that fixes a known vulnerability is proposed
	StatusHeld       = "held"       // a newer version exists but is too new (cooldown)
	StatusIgnored    = "ignored"    // policy or a person said to leave it
	StatusManual     = "manual"     // newer exists, but changing it is not something to automate
	StatusVulnerable = "vulnerable" // vulnerable, and no fixed version is available
	StatusTransitive = "transitive" // only a lock file lists it
	StatusUnknown    = "unknown"    // the source could not answer
)

// Update is one dependency's state and, when there is one, its proposed change.
type Update struct {
	Dep        depupdate.Dep `json:"dep"`
	Status     string        `json:"status"`
	Reason     string        `json:"reason,omitempty"`      // plain words for the Dependencies view
	Note       string        `json:"note,omitempty"`        // worth knowing; does not change the status
	Latest     string        `json:"latest,omitempty"`      // newest stable version published
	Target     string        `json:"target,omitempty"`      // what to propose
	TargetPath string        `json:"target_path,omitempty"` // Go: module path of the target when it differs
	Digest     string        `json:"digest,omitempty"`      // container and workflow: digest or commit of the target
	Kind       version.Kind  `json:"kind,omitempty"`
	Security   bool          `json:"security,omitempty"`
	Advisories []osv.Match   `json:"advisories,omitempty"`
	Published  time.Time     `json:"published,omitzero"`
	NotesURL   string        `json:"notes_url,omitempty"`
	HeldUntil  time.Time     `json:"held_until,omitzero"`
}

// AgeDays is how old the proposed version is, or -1 when unknown.
func (u Update) AgeDays(now time.Time) int {
	if u.Published.IsZero() {
		return -1
	}
	return int(now.Sub(u.Published).Hours() / 24)
}

// Resolver finds what each dependency should move to.
type Resolver struct {
	Sources map[string]source.Source // by ecosystem
	Policy  Policy
	Now     func() time.Time
	// Closed lists what people closed or asked to ignore: "ecosystem|name|target-major-or-version-or-*".
	Closed map[string]bool
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Resolve resolves every dependency. Dependencies that only a lock file
// lists are reported (with their vulnerabilities) but never proposed.
func (r *Resolver) Resolve(ctx context.Context, deps []depupdate.Dep, vulns []osv.Match) []Update {
	byKey := map[string][]osv.Match{}
	for _, m := range vulns {
		byKey[m.Dep.Key()] = append(byKey[m.Dep.Key()], m)
	}
	var out []Update
	for _, d := range deps {
		u := Update{Dep: d, Advisories: byKey[d.Key()]}
		if !d.Direct {
			u.Status = StatusTransitive
			if len(u.Advisories) > 0 {
				u.Security = true
				u.Status = StatusVulnerable
				u.Reason = "A package needed by another package is affected; update the package that needs it."
			}
			out = append(out, u)
			continue
		}
		r.one(ctx, &u)
		out = append(out, u)
	}
	return out
}

type cand struct {
	ver  string
	rel  source.Release
	kind version.Kind
	cmp  func(a, b cand) int
}

func (r *Resolver) one(ctx context.Context, u *Update) {
	d := u.Dep
	src := r.Sources[d.Ecosystem]
	if src == nil {
		u.Status, u.Reason = StatusUnknown, "No version source is configured for this ecosystem."
		return
	}
	if d.Ecosystem == depupdate.Container && d.Constraint == "" && d.Digest == "" {
		u.Status, u.Reason = StatusManual, "The image has no tag: it is whatever is current when it is pulled. Pin a version first."
		return
	}
	cur := d.Current
	if d.Ecosystem == depupdate.NPM && cur == "" {
		cur = lowerBound(d.Constraint)
	}
	if cur == "" {
		u.Status, u.Reason = StatusUnknown, "The version in use is not known: there is no lock file or the requirement is not pinned."
		return
	}
	rels, err := src.Versions(ctx, d.Name)
	if err != nil {
		u.Status, u.Reason = StatusUnknown, "The version source did not answer: "+err.Error()
		return
	}
	cands, major := r.candidates(d, cur, rels)
	if len(cands) == 0 && len(u.Advisories) > 0 {
		u.Latest, u.Security, u.Status = cur, true, StatusVulnerable
		u.Reason = "Known vulnerability and no fixed version is published yet."
		return
	}
	if len(cands) == 0 {
		u.Latest = cur
		u.Status = StatusUpToDate
		if major != "" {
			u.Latest = major
			u.Status, u.Reason = StatusManual, "A new major version ("+major+") has a different module path, so the code's imports must change. Update it by hand."
		}
		r.digestRefresh(ctx, u, src)
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].cmp(cands[i], cands[j]) > 0 })
	u.Latest = cands[0].ver
	if major != "" && u.Latest != major && d.Ecosystem == depupdate.Go {
		u.Note = "A new major version (" + major + ") also exists; it changes the module path and is left for a person."
	}
	// security first
	if len(u.Advisories) > 0 {
		u.Security = true
		if r.securityTarget(ctx, u, cands, src) {
			return
		}
		u.Status = StatusVulnerable
		u.Reason = "Known vulnerability and no fixed version is published yet."
		if r.ignored(u, cands[0]) {
			u.Status, u.Reason = StatusIgnored, "Vulnerable, but updates for this dependency are ignored: "+r.ignoreReason(u)
		}
		return
	}
	for _, c := range cands { // highest first
		if reason := r.excluded(d, cur, c); reason != "" {
			if c.ver == cands[0].ver && u.Target == "" {
				u.Reason = reason
			}
			continue
		}
		rel, err := src.Detail(ctx, d.Name, c.ver)
		if err == nil {
			c.rel = rel
		}
		if wait, until := r.tooNew(c.rel); wait {
			u.Status, u.HeldUntil = StatusHeld, until
			u.Reason = fmt.Sprintf("%s was published %s; the wait of %d days ends %s.", c.ver, c.rel.Time.Format("2006-01-02"), r.Policy.CooldownDays, until.Format("2006-01-02"))
			continue
		}
		if c.rel.Time.IsZero() && r.Policy.CooldownDays > 0 && d.Ecosystem != depupdate.Workflow {
			u.Status, u.Reason = StatusHeld, c.ver+" has no publication time, so its age cannot be checked; it is not proposed."
			continue
		}
		u.Target, u.Kind, u.Published, u.NotesURL, u.Digest = c.ver, c.kind, c.rel.Time, c.rel.NotesURL, c.rel.Digest
		u.Status, u.Reason, u.HeldUntil = StatusUpdate, "", time.Time{}
		return
	}
	if u.Status == "" {
		u.Status = StatusIgnored
		if u.Reason == "" {
			u.Reason = "Every newer version is excluded by the update settings."
		}
	}
}

func (r *Resolver) tooNew(rel source.Release) (bool, time.Time) {
	if rel.Time.IsZero() || r.Policy.CooldownDays <= 0 {
		return false, time.Time{}
	}
	until := rel.Time.Add(time.Duration(r.Policy.CooldownDays) * 24 * time.Hour)
	return r.now().Before(until), until
}

// securityTarget picks the lowest candidate that is at least the highest
// fixed version of every advisory and is not itself affected.
func (r *Resolver) securityTarget(ctx context.Context, u *Update, cands []cand, src source.Source) bool {
	d := u.Dep
	fixed := ""
	for _, a := range u.Advisories {
		if a.FixedIn == "" {
			continue
		}
		if fixed == "" || cands[0].cmp(cand{ver: fixed}, cand{ver: a.FixedIn}) < 0 {
			fixed = a.FixedIn
		}
	}
	if fixed == "" {
		return false
	}
	asc := append([]cand(nil), cands...)
	sort.Slice(asc, func(i, j int) bool { return asc[i].cmp(asc[i], asc[j]) < 0 })
	for _, c := range asc {
		if c.cmp(c, cand{ver: fixed}) < 0 {
			continue
		}
		affected := false
		for _, a := range u.Advisories {
			if ok, _ := osv.Affects(a.Vuln, osv.Ecosystem(d.Ecosystem), d.Name, c.ver); ok {
				affected = true
			}
		}
		if affected {
			continue
		}
		if r.ignored(u, c) {
			continue
		}
		if rel, err := src.Detail(ctx, d.Name, c.ver); err == nil {
			c.rel = rel
		}
		u.Target, u.Kind, u.Published, u.NotesURL, u.Digest = c.ver, c.kind, c.rel.Time, c.rel.NotesURL, c.rel.Digest
		u.Status = StatusSecurity
		u.Reason = fmt.Sprintf("%s is affected by %s; %s fixes it.", d.Current, advisoryList(u.Advisories), c.ver)
		return true
	}
	return false
}

func advisoryList(m []osv.Match) string {
	var ids []string
	for _, a := range m {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ", ")
}

// digestRefresh handles a digest-pinned image or action whose tag has not
// changed but whose content has.
func (r *Resolver) digestRefresh(ctx context.Context, u *Update, src source.Source) {
	d := u.Dep
	if d.Ecosystem != depupdate.Container || d.Digest == "" || d.Constraint == "" {
		return
	}
	rel, err := src.Detail(ctx, d.Name, d.Constraint)
	if err != nil || rel.Digest == "" || rel.Digest == d.Digest {
		return
	}
	if wait, until := r.tooNew(rel); wait {
		u.Status, u.HeldUntil, u.Reason = StatusHeld, until, "The tag was rebuilt recently; the wait ends "+until.Format("2006-01-02")+"."
		return
	}
	u.Target, u.Digest, u.Kind, u.Published = d.Constraint, rel.Digest, version.KindOther, rel.Time
	u.Status, u.Reason = StatusUpdate, "The tag "+d.Constraint+" now points at a newer build; the pinned digest is refreshed."
}

func lowerBound(c string) string {
	c = strings.TrimLeft(strings.TrimSpace(c), "^~>=v ")
	if v, ok := version.ParseSemver(c); ok && !strings.ContainsAny(c, " |x*") {
		return v.String()
	}
	return ""
}
