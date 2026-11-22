package source

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/starters"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// The custom steps of a library, for people choosing one: a list at a version,
// and a page for one step with its versions and what changed between them.

// StepSummary is one step in a library's list.
type StepSummary struct {
	stepkit.Info
	Problems []string `json:"problems,omitempty"`
}

// LibrarySteps is a library's steps at one version.
type LibrarySteps struct {
	Library   string        `json:"library"`
	Version   string        `json:"version"`
	Commit    string        `json:"commit"`
	Versions  []string      `json:"versions"`
	Steps     []StepSummary `json:"steps"`
	Truncated bool          `json:"truncated,omitempty"`
}

// StepVersion is one version of a step: where its manifest was the same, and
// what changed from the version before.
type StepVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Digest  string `json:"digest"`
	// Changes since the previous (older) version listed; Widens says that the step may do more than it did.
	Changes []StepChange `json:"changes,omitempty"`
	Widens  bool         `json:"widens,omitempty"`
}

// StepChange is one difference between two versions.
type StepChange struct {
	What       string `json:"what"`
	Permission bool   `json:"permission,omitempty"`
	Widening   bool   `json:"widening,omitempty"`
}

// StepPage is everything about one step of a library at a version.
type StepPage struct {
	Library  string        `json:"library"`
	Version  string        `json:"version"`
	Commit   string        `json:"commit"`
	Step     stepkit.Info  `json:"step"`
	Versions []StepVersion `json:"versions"` // tags, newest first
	// Newest is the newest tag, when it is not Version: the update on offer, with what it changes.
	Newest        string       `json:"newest,omitempty"`
	NewestChanges []StepChange `json:"newest_changes,omitempty"`
	NewestWidens  bool         `json:"newest_widens,omitempty"`
}

const maxLibrarySteps = 100

func changes(old, cur *stepkit.Manifest) (out []StepChange, widens bool) {
	for _, c := range stepkit.Diff(old, cur) {
		out = append(out, StepChange{What: c.What, Permission: c.Permission, Widening: c.Widening})
		widens = widens || c.Widening
	}
	return out, widens
}

// manifestAt reads and parses the manifest of a step at a commit; ErrNotFound if there is none.
func (lib *project) stepAt(ctx context.Context, sha, name string) (*stepkit.Manifest, error) {
	if !pipeline.IsCommitID(sha) || !pipeline.ValidStepName(name) {
		return nil, engine.ErrNotFound
	}
	lib.mu.Lock()
	data, err := lib.showAt(ctx, sha, stepkit.ManifestPath(name))
	lib.mu.Unlock()
	if errors.Is(err, pipeline.ErrNoTemplate) {
		return nil, fmt.Errorf("%w: the library has no step %s at that version", engine.ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	m, probs := stepkit.ParseManifest(data)
	if len(probs) > 0 {
		return nil, fmt.Errorf("%w: %s: %s", engine.ErrInvalid, stepkit.ManifestPath(name), probs[0].Error())
	}
	return m, nil
}

// versionsOf returns a library's tags, newest first, and its branches.
func (m *Manager) versionsOf(ctx context.Context, lib *project) (tags, all []string, err error) {
	lib.mu.Lock()
	err = lib.fetch(ctx, checkFresh)
	var refs []Ref
	if err == nil {
		refs, err = lib.refs(ctx)
	}
	lib.mu.Unlock()
	if err != nil {
		return nil, nil, errors.New(redact.URLs(err.Error()))
	}
	var branches []string
	for _, r := range refs {
		if r.Kind == "tag" {
			tags = append(tags, r.Name)
		} else {
			branches = append(branches, r.Name)
		}
	}
	sortNewestFirst(tags)
	all = append(append([]string{}, tags...), branches...)
	if len(all) > maxLibraryVersions {
		all = all[:maxLibraryVersions]
	}
	return tags, all, nil
}

// LibrarySteps lists the custom steps of a library at a version (default: the newest tag, else the default branch).
func (m *Manager) LibrarySteps(ctx context.Context, name, version string) (*LibrarySteps, error) {
	if name == starters.Name {
		out := &LibrarySteps{Library: name, Version: starters.Version, Commit: starters.Commit(), Versions: []string{starters.Version}, Steps: []StepSummary{}}
		for _, n := range starters.Steps() {
			data, _ := starters.Step(n)
			if mf, probs := stepkit.ParseManifest(data); len(probs) == 0 {
				out.Steps = append(out.Steps, StepSummary{Info: mf.Describe(name + "/" + n)})
			}
		}
		return out, nil
	}
	lib := m.c().libraries[name]
	if lib == nil {
		return nil, fmt.Errorf("%w: library %q (the libraries are: %s)", engine.ErrNotFound, name, m.c().libraryNames())
	}
	tags, all, err := m.versionsOf(ctx, lib)
	if err != nil {
		return nil, err
	}
	out := &LibrarySteps{Library: name, Versions: all, Steps: []StepSummary{}}
	if version == "" {
		switch {
		case len(tags) > 0:
			version = tags[0]
		case len(all) > 0:
			version = all[0]
		default:
			return out, nil
		}
	}
	sha, _, err := lib.resolveVersion(ctx, version, checkFresh)
	if err != nil {
		return nil, err
	}
	out.Version, out.Commit = version, sha
	lib.mu.Lock()
	list, err := gitOut(ctx, lib.dir, "ls-tree", "--name-only", sha, stepkit.StepDir+"/")
	lib.mu.Unlock()
	if err != nil {
		return nil, errors.New(redact.URLs(err.Error()))
	}
	for _, p := range strings.Split(strings.TrimSpace(list), "\n") {
		stepName := path.Base(p)
		if !pipeline.ValidStepName(stepName) {
			continue
		}
		if len(out.Steps) == maxLibrarySteps {
			out.Truncated = true
			break
		}
		mf, err := lib.stepAt(ctx, sha, stepName)
		if err != nil {
			if errors.Is(err, engine.ErrInvalid) {
				out.Steps = append(out.Steps, StepSummary{Info: stepkit.Info{Name: stepName}, Problems: []string{err.Error()}})
			}
			continue
		}
		out.Steps = append(out.Steps, StepSummary{Info: mf.Describe(name + "/" + stepName)})
	}
	return out, nil
}

// LibraryStep is the page of one step of a library: its manifest at a
// version, its tagged versions with what changed in each, and the update on
// offer.
func (m *Manager) LibraryStep(ctx context.Context, library, step, version string) (*StepPage, error) {
	if library == starters.Name {
		data, ok := starters.Step(step)
		if !ok {
			return nil, fmt.Errorf("%w: the library %s has no step %s", engine.ErrNotFound, library, step)
		}
		mf, probs := stepkit.ParseManifest(data)
		if len(probs) > 0 {
			return nil, fmt.Errorf("%w: %s", engine.ErrInvalid, probs[0].Error())
		}
		return &StepPage{Library: library, Version: starters.Version, Commit: starters.Commit(), Step: mf.Describe(library + "/" + step), Versions: []StepVersion{}}, nil
	}
	lib := m.c().libraries[library]
	if lib == nil {
		return nil, fmt.Errorf("%w: library %q (the libraries are: %s)", engine.ErrNotFound, library, m.c().libraryNames())
	}
	tags, all, err := m.versionsOf(ctx, lib)
	if err != nil {
		return nil, err
	}
	if version == "" {
		switch {
		case len(tags) > 0:
			version = tags[0]
		case len(all) > 0:
			version = all[0]
		default:
			return nil, fmt.Errorf("%w: the library %s has no versions", engine.ErrNotFound, library)
		}
	}
	sha, _, err := lib.resolveVersion(ctx, version, checkFresh)
	if err != nil {
		return nil, err
	}
	cur, err := lib.stepAt(ctx, sha, step)
	if err != nil {
		return nil, err
	}
	page := &StepPage{Library: library, Version: version, Commit: sha, Step: cur.Describe(library + "/" + step), Versions: []StepVersion{}}
	// The versions in which the step exists, newest first, each with what changed from the older one.
	type found struct {
		tag, sha string
		m        *stepkit.Manifest
	}
	var have []found
	for _, t := range tags {
		tsha, _, err := lib.resolveVersion(ctx, t, checkFresh)
		if err != nil {
			continue
		}
		if mf, err := lib.stepAt(ctx, tsha, step); err == nil {
			have = append(have, found{t, tsha, mf})
		}
	}
	for i, f := range have {
		v := StepVersion{Version: f.tag, Commit: f.sha, Digest: f.m.Digest}
		if i+1 < len(have) {
			v.Changes, v.Widens = changes(have[i+1].m, f.m)
		}
		page.Versions = append(page.Versions, v)
	}
	if len(have) > 0 && have[0].tag != version {
		page.Newest = have[0].tag
		page.NewestChanges, page.NewestWidens = changes(cur, have[0].m)
	}
	return page, nil
}

// StepUpdateNotice compares the version of a step a pipeline is pinned to with the newest tag of its library.
func (m *Manager) StepUpdateNotice(ctx context.Context, library, step, pinnedCommit string) (newest string, cs []StepChange, widens bool, err error) {
	lib := m.c().libraries[library]
	if lib == nil {
		return "", nil, false, fmt.Errorf("%w: library %q", engine.ErrNotFound, library)
	}
	tags, _, err := m.versionsOf(ctx, lib)
	if err != nil || len(tags) == 0 {
		return "", nil, false, err
	}
	cur, err := lib.stepAt(ctx, pinnedCommit, step)
	if err != nil {
		return "", nil, false, err
	}
	tsha, _, err := lib.resolveVersion(ctx, tags[0], checkFresh)
	if err != nil || tsha == pinnedCommit {
		return "", nil, false, err
	}
	latest, err := lib.stepAt(ctx, tsha, step)
	if err != nil {
		return "", nil, false, nil
	}
	if latest.Digest == cur.Digest {
		return "", nil, false, nil
	}
	cs, widens = changes(cur, latest)
	return tags[0], cs, widens, nil
}

// sortNewestFirst orders tags such as v1.2, v1.10 and v2 by their numbers, newest first, so that "the newest tag"
// does not depend on the order Git lists them in (tags made in the same second have no order of their own).
func sortNewestFirst(tags []string) {
	sort.SliceStable(tags, func(i, j int) bool { return versionLess(tags[j], tags[i]) })
}

func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		switch {
		case pa[i].num && pb[i].num && pa[i].n != pb[i].n:
			return pa[i].n < pb[i].n
		case !(pa[i].num && pb[i].num) && pa[i].s != pb[i].s:
			return pa[i].s < pb[i].s
		}
	}
	return len(pa) < len(pb)
}

type versionPart struct {
	num bool
	n   int
	s   string
}

func versionParts(v string) []versionPart {
	var out []versionPart
	for i := 0; i < len(v); {
		j := i
		digit := v[i] >= '0' && v[i] <= '9'
		for j < len(v) && (v[j] >= '0' && v[j] <= '9') == digit {
			j++
		}
		p := versionPart{s: v[i:j]}
		if digit && j-i < 9 {
			p.num = true
			for _, c := range v[i:j] {
				p.n = p.n*10 + int(c-'0')
			}
		}
		out = append(out, p)
		i = j
	}
	return out
}
