package graph

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
)

// RequirementsSource works out what each component of a repository needs:
// what the requirements file declares, and what can safely be inferred (the
// architectures the pipeline builds for, the ports a Containerfile exposes,
// the resources Kubernetes manifests ask for, the runtime version and the
// data services the manifests point to). A declared fact always wins; an
// inferred fact says so and says what it was read from; an inferred fact that
// differs from a declared one is kept as advice and never applied.
type RequirementsSource struct {
	Repos Repos
	Cfg   Config
}

// Name is "requirements".
func (RequirementsSource) Name() string { return "requirements" }

// Scopes is repositories.
func (RequirementsSource) Scopes() []string { return []string{ScopeRepository} }

// RequirementKey is the key of a requirement node.
func RequirementKey(component string, f Fact) string {
	return component + "#" + f.Facet + "=" + f.Value
}

// Assert reads the repository.
func (r RequirementsSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	tree, commit, _, err := repoFiles(ctx, r.Repos, sc.Ref, append(append([]string{}, ExtraGlobs...), RequirementsPath))
	if err != nil {
		return a, "", err
	}
	var decl *Requirements
	if data, err := fs.ReadFile(tree, RequirementsPath); err == nil {
		if decl, err = ParseRequirements(data); err != nil {
			return a, commit, err // the file is wrong: what the graph holds stays, and the problem is reported
		}
	}
	sub := RepoSubject(sc.Ref)
	for _, u := range Units(tree, sc.Ref, r.Cfg) {
		comp := NodeRef{KindComponent, u.Key}
		facts, advice := MergeFacts(declaredFacts(decl, u, sc.Ref), u.Facts)
		for _, f := range facts {
			a.addRequirement(comp, f, sub, commit, nil)
		}
		for _, ad := range advice {
			// Advice is kept as a requirement that is not applied.
			f := Fact{Facet: ad.Facet, Value: strings.TrimSpace(ad.Inferred), Source: Inferred, Evidence: ad.Evidence}
			f.Value = findInferred(u.Facts, ad)
			a.addRequirement(comp, f, sub, commit, map[string]any{"advice": true, "declared": ad.Declared, "message": ad.Message})
		}
	}
	return a, commit, nil
}

func findInferred(fs []Fact, ad Advice) string {
	for _, f := range fs {
		if f.Facet == ad.Facet && f.Display() == ad.Inferred {
			return f.Value
		}
	}
	return ad.Inferred
}

func (a *Assertion) addRequirement(comp NodeRef, f Fact, sub, commit string, extra map[string]any) {
	key := RequirementKey(comp.Key, f)
	attrs := map[string]any{"facet": f.Facet, "value": f.Value, "display": f.Display(), "source": string(f.Source), "component": comp.Key}
	if f.Evidence != "" {
		attrs["evidence"] = f.Evidence
	}
	for k, v := range extra {
		attrs[k] = v
	}
	conf := f.Source
	a.Add(Node{Kind: KindRequirement, Key: key, Name: fmt.Sprintf("%s %s", f.Facet, f.Display()), Subject: sub, Attrs: attrs, Confidence: conf})
	a.Link(Edge{Kind: EdgeNeeds, From: comp, To: NodeRef{KindRequirement, key}, Qualifier: f.Facet + "=" + f.Value, Subject: sub, Ref: commit, Confidence: conf})
}
