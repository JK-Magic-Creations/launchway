package kube

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChartRepoURL and ChartPath say where the chart lives in Git, for a GitOps controller to pull it.
const (
	ChartRepoURL = "https://github.com/JK-Magic-Creations/launchway"
	ChartPath    = "deploy/helm/launchway"
)

// GitOpsFiles are the files written for someone to commit, in place of applying.
type GitOpsFiles struct {
	Values   string // the small values file
	Manifest string // the Application (Argo CD) or HelmRelease (Flux)
	Name     string // file name of Manifest
}

// WriteGitOps builds the files for a controller. revision is the chart's Git tag or branch to follow.
func WriteGitOps(s Settings, controller, revision string) (GitOpsFiles, error) {
	vb, err := s.YAML()
	if err != nil {
		return GitOpsFiles{}, err
	}
	out := GitOpsFiles{Values: string(vb)}
	switch controller {
	case "argocd":
		app := map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": s.Release, "namespace": "argocd"},
			"spec": map[string]any{
				"project": "default",
				"source": map[string]any{"repoURL": ChartRepoURL, "targetRevision": revision, "path": ChartPath,
					"helm": map[string]any{"valuesObject": s.Values()}},
				"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": s.Namespace},
				"syncPolicy": map[string]any{"automated": map[string]any{"prune": false, "selfHeal": true},
					"syncOptions": []any{"CreateNamespace=true", "ServerSideApply=true"}},
			},
		}
		b, err := yaml.Marshal(app)
		if err != nil {
			return out, err
		}
		out.Name = "launchway-application.yaml"
		out.Manifest = "# An Argo CD Application for Launchway. The chart makes its own secrets in a hook Job that Argo runs as PreSync;\n# nothing secret is in this file. Commit it, then apply it once (kubectl apply -f) or let your app-of-apps pick it up.\n" + string(b)
	case "flux":
		src := map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "metadata": map[string]any{"name": "launchway", "namespace": "flux-system"},
			"spec": map[string]any{"interval": "10m", "url": ChartRepoURL, "ref": map[string]any{"tag": revision}}}
		hr := map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"name": s.Release, "namespace": s.Namespace},
			"spec": map[string]any{"interval": "10m", "install": map[string]any{"createNamespace": true},
				"chart":  map[string]any{"spec": map[string]any{"chart": ChartPath, "sourceRef": map[string]any{"kind": "GitRepository", "name": "launchway", "namespace": "flux-system"}}},
				"values": s.Values()}}
		a, err := yaml.Marshal(src)
		if err != nil {
			return out, err
		}
		b, err := yaml.Marshal(hr)
		if err != nil {
			return out, err
		}
		out.Name = "launchway-helmrelease.yaml"
		out.Manifest = "# A Flux GitRepository and HelmRelease for Launchway. Nothing secret is in this file.\n" + string(a) + "---\n" + string(b)
	default:
		return out, fmt.Errorf("unknown GitOps controller %q", controller)
	}
	return out, nil
}

// ImageRefParts splits an image reference into repository, tag and digest.
func ImageRefParts(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], "", ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:], ""
	}
	return ref, "", ""
}
