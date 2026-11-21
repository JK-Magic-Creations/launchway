package plan

import (
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// candidates returns the releases newer than cur that are fair game for this
// dependency, with the kind of each. For Go, major is the newest version at a
// higher major module path ("" when none).
func (r *Resolver) candidates(d depupdate.Dep, cur string, rels []source.Release) (out []cand, major string) {
	switch d.Ecosystem {
	case depupdate.Go, depupdate.NPM:
		return r.semverCandidates(cur, rels)
	case depupdate.Python:
		return r.pepCandidates(cur, rels), ""
	case depupdate.Container, depupdate.Workflow:
		return r.tagCandidates(cur, rels), ""
	}
	return nil, ""
}

func (r *Resolver) semverCandidates(cur string, rels []source.Release) (out []cand, major string) {
	curV, ok := version.ParseSemver(cur)
	if !ok {
		return nil, ""
	}
	cmp := func(a, b cand) int {
		x, _ := version.ParseSemver(a.ver)
		y, _ := version.ParseSemver(b.ver)
		return x.Compare(y)
	}
	var best version.Semver
	for _, rel := range rels {
		v, ok := version.ParseSemver(rel.Version)
		if !ok || rel.Yanked || rel.Deprecated != "" || v.IsPseudo() || (v.IsPre() && !r.Policy.Prereleases && !curV.IsPre()) {
			continue
		}
		if rel.Module != "" {
			if best.Raw == "" || v.Compare(best) > 0 {
				best, major = v, rel.Version
			}
			continue
		}
		if v.Compare(curV) > 0 {
			out = append(out, cand{ver: rel.Version, rel: rel, kind: version.KindOf(curV, v), cmp: cmp})
		}
	}
	return out, major
}

func (r *Resolver) pepCandidates(cur string, rels []source.Release) (out []cand) {
	curV, ok := version.ParsePEP440(cur)
	if !ok {
		return nil
	}
	cmp := func(a, b cand) int {
		x, _ := version.ParsePEP440(a.ver)
		y, _ := version.ParsePEP440(b.ver)
		return x.Compare(y)
	}
	for _, rel := range rels {
		v, ok := version.ParsePEP440(rel.Version)
		if !ok || rel.Yanked || (v.IsPre() && !r.Policy.Prereleases && !curV.IsPre()) || v.Compare(curV) <= 0 || v.Local != "" {
			continue
		}
		out = append(out, cand{ver: rel.Version, rel: rel, kind: version.KindOfPEP440(curV, v), cmp: cmp})
	}
	return out
}

func (r *Resolver) tagCandidates(cur string, rels []source.Release) (out []cand) {
	curT := version.ParseTag(cur)
	if !curT.OK {
		return nil
	}
	cmp := func(a, b cand) int { return version.ParseTag(a.ver).Compare(version.ParseTag(b.ver)) }
	for _, rel := range rels {
		t := version.ParseTag(rel.Version)
		if !curT.SameFamily(t) || (t.Pre && !curT.Pre && !r.Policy.Prereleases) || t.Compare(curT) <= 0 {
			continue
		}
		out = append(out, cand{ver: rel.Version, rel: rel, kind: version.KindOfTag(curT, t), cmp: cmp})
	}
	return out
}

// excluded says why policy leaves a candidate out ("" when it may be proposed).
func (r *Resolver) excluded(d depupdate.Dep, cur string, c cand) string {
	p := r.Policy
	if len(p.Allow) > 0 && !AnyGlob(p.Allow, d.Name) {
		return "The dependency is not on the allow list."
	}
	if why := excludedByIgnore(p.Ignore, d, c); why != "" {
		return why
	}
	if why := excludedByConstraint(p.Constraints, d, c); why != "" {
		return why
	}
	return r.excludedByClosing(d, c)
}

func excludedByIgnore(rules []Ignore, d depupdate.Dep, c cand) string {
	for _, ig := range rules {
		if !Glob(ig.Name, d.Name) {
			continue
		}
		if len(ig.Kinds) > 0 && !contains(ig.Kinds, string(c.kind)) {
			continue
		}
		if ig.Versions != "" {
			rg, ok := version.ParseRange(ig.Versions)
			v, vok := version.ParseSemver(c.ver)
			if !ok || !vok || !rg.Contains(v) {
				continue
			}
		}
		return "Ignored by the update settings" + reasonSuffix(ig.Reason)
	}
	return ""
}

func excludedByConstraint(rules []Constraint, d depupdate.Dep, c cand) string {
	for _, k := range rules {
		if !Glob(k.Name, d.Name) {
			continue
		}
		if k.Major != nil && majorOf(c.ver) != strconv.Itoa(*k.Major) {
			return "Kept on major version " + strconv.Itoa(*k.Major) + " by the update settings."
		}
		if v, ok := version.ParseSemver(c.ver); ok && k.Range != "" {
			if rg, ok := version.ParseRange(k.Range); ok && !rg.Contains(v) {
				return "Outside the range " + k.Range + " set by the update settings."
			}
		}
	}
	return ""
}

func (r *Resolver) excludedByClosing(d depupdate.Dep, c cand) string {
	if r.Closed == nil {
		return ""
	}
	if r.Closed[d.Ecosystem+"|"+d.Name+"|*"] || r.Closed[d.Ecosystem+"|"+d.Name+"|major-"+majorOf(c.ver)] {
		return "A person closed an earlier pull request for this and asked not to be shown it again."
	}
	// "ignore this version" also covers every version below it: nobody wants the
	// one just under the version they turned down.
	prefix := d.Ecosystem + "|" + d.Name + "|"
	for k := range r.Closed {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		ig := strings.TrimPrefix(k, prefix)
		if ig == "*" || strings.HasPrefix(ig, "major-") {
			continue
		}
		if c.cmp(c, cand{ver: ig}) <= 0 {
			return "A person closed an earlier pull request for this version or a newer one and asked not to be shown it again."
		}
	}
	return ""
}

func (r *Resolver) ignored(u *Update, c cand) bool { return r.excluded(u.Dep, u.Dep.Current, c) != "" }

func (r *Resolver) ignoreReason(u *Update) string {
	for _, ig := range r.Policy.Ignore {
		if Glob(ig.Name, u.Dep.Name) && ig.Reason != "" {
			return ig.Reason
		}
	}
	return "see the update settings"
}

func reasonSuffix(s string) string {
	if s == "" {
		return "."
	}
	return ": " + s
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func majorOf(v string) string {
	if s, ok := version.ParseSemver(v); ok {
		return strconv.FormatUint(s.Major, 10)
	}
	if t := version.ParseTag(v); t.OK && len(t.Nums) > 0 {
		return strconv.FormatUint(t.Nums[0], 10)
	}
	if p, ok := version.ParsePEP440(v); ok && len(p.Release) > 0 {
		return strconv.FormatUint(p.Release[0], 10)
	}
	return strings.TrimPrefix(v, "v")
}
