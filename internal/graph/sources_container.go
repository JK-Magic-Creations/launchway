package graph

import (
	"context"
	"path"
	"strings"
)

// ContainerSource reads Containerfiles and the pipeline file: which base
// image each stage starts from, which pipeline builds which image, and for
// which platforms. These are the plan; what was actually built is the Sortie
// source's.
type ContainerSource struct {
	Repos Repos
	Cfg   Config
}

// Name is "containers".
func (ContainerSource) Name() string { return "containers" }

// Scopes is repositories.
func (ContainerSource) Scopes() []string { return []string{ScopeRepository} }

// StreamKey is the key of the image node for a pushed name: the registry's
// host and the repository path.
func (c Config) StreamKey(registry, repository string) string {
	host := registry
	if h, ok := c.RegistryHosts[registry]; ok && h != "" {
		host = h
	}
	if registry == "" {
		return NormalizeImage(repository)
	}
	return NormalizeImage(host + "/" + repository)
}

// PipelineKey is the key of a repository's pipeline node.
func PipelineKey(repo string) string { return repo + ":pipeline" }

// Assert reads the repository.
func (c ContainerSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	tree, commit, _, err := repoFiles(ctx, c.Repos, sc.Ref, ExtraGlobs)
	if err != nil {
		return a, "", err
	}
	sub := RepoSubject(sc.Ref)
	repo := NodeRef{KindRepository, sc.Ref}
	a.Add(Node{Kind: KindRepository, Key: sc.Ref, Name: sc.Ref, Subject: sub, Attrs: map[string]any{"commit": commit}})
	cfs := ScanContainerfiles(tree)
	byPath := map[string]NodeRef{}
	for _, cf := range cfs {
		key := componentKey(sc.Ref, cf.Path)
		ref := NodeRef{KindComponent, key}
		byPath[cf.Path] = ref
		attrs := map[string]any{"kind": "containerfile", "file": cf.Path, "dir": path.Dir(cf.Path)}
		if len(cf.Ports) > 0 {
			attrs["ports"] = cf.Ports
		}
		a.Add(Node{Kind: KindComponent, Key: key, Name: sc.Ref + "/" + cf.Path, Subject: sub, Attrs: attrs})
		a.Link(Edge{Kind: EdgeContains, From: repo, To: ref, Subject: sub, Ref: commit})
		for _, st := range cf.Stages {
			if st.Base == "" {
				continue
			}
			a.Add(Node{Kind: KindImage, Key: st.Base, Name: st.Base, Attrs: map[string]any{"role": "base"}})
			attrs := map[string]any{"stage": st.Name, "stage_index": st.Index, "final": st.Final, "line": st.Line}
			resolved := ""
			if st.Digest != "" {
				resolved = st.Digest
			}
			a.Link(Edge{Kind: EdgeBuiltFrom, From: ref, To: NodeRef{KindImage, st.Base}, Qualifier: stageQualifier(st), Constraint: st.Tag, Resolved: resolved, Attrs: attrs, Subject: sub, Ref: commit})
		}
	}
	if pf, ok := ScanPipeline(tree); ok {
		pk := NodeRef{KindPipeline, PipelineKey(sc.Ref)}
		a.Add(Node{Kind: KindPipeline, Key: pk.Key, Name: firstNonEmpty(pf.Name, sc.Ref+" pipeline"), Subject: sub})
		a.Link(Edge{Kind: EdgeDefinedIn, From: pk, To: repo, Subject: sub, Ref: commit})
		for _, is := range pf.Images {
			for _, t := range is.Targets {
				reg, rp, _ := strings.Cut(t, "/")
				key := c.Cfg.StreamKey(reg, rp)
				stream := NodeRef{KindImage, key}
				a.Add(Node{Kind: KindImage, Key: key, Name: key, Attrs: map[string]any{"role": "built", "platforms": is.Platforms}})
				a.Link(Edge{Kind: EdgeBuilds, From: pk, To: stream, Qualifier: is.Step, Attrs: map[string]any{"platforms": is.Platforms, "context": is.Context, "file": is.File}, Subject: sub, Ref: commit})
				if cf, ok := byPath[is.File]; ok {
					a.Link(Edge{Kind: EdgeBuiltFrom, From: stream, To: cf, Qualifier: is.Step, Subject: sub, Ref: commit})
				}
			}
		}
	}
	return a, commit, nil
}

func stageQualifier(st Stage) string {
	if st.Name != "" {
		return "stage:" + st.Name
	}
	return "stage:" + itoa(st.Index)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
