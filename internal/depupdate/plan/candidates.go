package plan

import (
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
			if !ok || rel.Yanked || rel.Deprecated != "" || (v.IsPre() && (!r.Policy.Prereleases && !curV.IsPre())) || v.IsPseudo() {
				continue
			}
			if rel.Module != "" {
				if best.Raw == "" || v.Compare(best) > 0 {
					best = v
					major = rel.Version
				}
				continue
			}
			if v.Compare(curV) <= 0 {
				continue
			}
			out = append(out, cand{ver: rel.Version, rel: rel, kind: version.KindOf(curV, v), cmp: cmp})
		}
	case depupdate.Python:
		curV, ok := version.ParsePEP440(cur)
		if !ok {
			return nil, ""
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
	case depupdate.Container, depupdate.Workflow:
		curT := version.ParseTag(cur)
		if !curT.OK {
			return nil, ""
		}
		cmp := func(a, b cand) int { return version.ParseTag(a.ver).Compare(version.ParseTag(b.ver)) }
		for _, rel := range rels {
			t := version.ParseTag(rel.Version)
			if !curT.SameFamily(t) || (t.Pre && !curT.Pre && !r.Policy.Prereleases) || t.Compare(curT) <= 0 {
				continue
			}
			out = append(out, cand{ver: rel.Version, rel: rel, kind: version.KindOfTag(curT, t), cmp: cmp})
		}
	}
	return out, major
}

// excluded says why policy leaves a candidate out ("" when it may be proposed).
func (r *Resolver) excluded(d depupdate.Dep, cur string, c cand) string {
	p := r.Policy
	if len(p.Allow) > 0 && !AnyGlob(p.Allow, d.Name) {
		return "The dependency is not on the allow list."
	}
	for _, ig := range p.Ignore {
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
	for _, k := range p.Constraints {
		if !Glob(k.Name, d.Name) {
			continue
		}
		v, ok := version.ParseSemver(c.ver)
		if !ok {
			t := version.ParseTag(c.ver)
			if t.OK && len(t.Nums) > 0 && k.Major != nil && int(t.Nums[0]) != *k.Major {
				return "Kept on major version " + itoa(*k.Major) + " by the update settings."
			}
			continue
		}
		if k.Major != nil && int(v.Major) != *k.Major {
			return "Kept on major version " + itoa(*k.Major) + " by the update settings."
		}
		if k.Range != "" {
			if rg, ok := version.ParseRange(k.Range); ok && !rg.Contains(v) {
				return "Outside the range " + k.Range + " set by the update settings."
			}
		}
	}
	if r.Closed != nil {
		for _, key := range []string{
			d.Ecosystem + "|" + d.Name + "|" + c.ver,
			d.Ecosystem + "|" + d.Name + "|major-" + majorOf(c.ver),
			d.Ecosystem + "|" + d.Name + "|*",
		} {
			if r.Closed[key] {
				return "A person closed an earlier pull request for this and asked not to be shown it again."
			}
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
		return itoa(int(s.Major))
	}
	if t := version.ParseTag(v); t.OK && len(t.Nums) > 0 {
		return itoa(int(t.Nums[0]))
	}
	if p, ok := version.ParsePEP440(v); ok && len(p.Release) > 0 {
		return itoa(int(p.Release[0]))
	}
	return strings.TrimPrefix(v, "v")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
