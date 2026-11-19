package imagebuild

import (
	"encoding/json"
	"slices"
	"time"
)

// Launchway's provenance for an image: an in-toto statement with a SLSA
// v1 predicate, written by the runner from what it knows first hand.
//
// What it vouches for, and how far:
//
//   - the source: the repository and commit the runner checked out, the
//     ref and event that started the run, and the libraries (with their
//     commits) the pipeline used. The runner checked these out itself.
//   - the build's parameters: Dockerfile, target, platforms, build
//     arguments (their values: never put secrets in build arguments) and
//     the IDs, never the values, of the build secrets.
//   - who built it: the runner, the builders, the run and step attempt.
//   - what it was built from, as far as the builder could tell: the base
//     images by digest, from the builder's own provenance.
//
// It does not vouch that the build was hermetic. A Dockerfile runs code
// that may download anything; the builder's provenance (attached to the
// image in its index) says whether it saw network access, and lists the
// build context as a local source it cannot describe further. Earlier steps
// of the run also run user code and may change the workspace the image is
// built from: the source is "the commit, as the run's earlier steps left
// it". The statement is signed only when the step signs, and a signature is
// only as trustworthy as access to the key: a runner's own key (sign: true)
// is never given to steps; a key the pipeline names is readable by any
// step of a project its store allows.
type provenanceInput struct {
	BuilderID    string // the runner, as a URI
	InvocationID string
	Started      time.Time
	Finished     time.Time
	Source       Source
	Image        buildParams
	Builders     []string
	BaseImages   []resolvedDep
	Subjects     []subject
	Version      string // Launchway's
}

// Source is where the code came from, as the runner saw it.
type Source struct {
	Repository string    `json:"repository,omitempty"` // credentials removed
	Ref        string    `json:"ref,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	Event      string    `json:"event,omitempty"`
	Project    string    `json:"project,omitempty"`
	Pipeline   string    `json:"pipeline,omitempty"`
	Run        string    `json:"run,omitempty"`
	RunNumber  int       `json:"run_number,omitempty"`
	Step       string    `json:"step,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	RunURL     string    `json:"run_url,omitempty"`
	Libraries  []Library `json:"libraries,omitempty"`
}

// Library is a shared library a run used, at the commit it used.
type Library struct {
	Name       string `json:"name"`
	Repository string `json:"repository,omitempty"`
	Version    string `json:"version,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

type buildParams struct {
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"`
	Target     string            `json:"target,omitempty"`
	Platforms  []string          `json:"platforms,omitempty"`
	BuildArgs  map[string]string `json:"build_args,omitempty"`
	Secrets    []string          `json:"secrets,omitempty"` // IDs only
	Tags       []string          `json:"tags,omitempty"`
}

type resolvedDep struct {
	URI    string            `json:"uri,omitempty"`
	Name   string            `json:"name,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

// BuildType is the buildType of Launchway's provenance.
const BuildType = "https://launchway.dev/image-step/v1"

// SLSAProvenanceV1 is the predicate type of SLSA provenance v1.
const SLSAProvenanceV1 = "https://slsa.dev/provenance/v1"

// CycloneDXPredicate is the predicate type cosign uses for CycloneDX SBOMs.
const CycloneDXPredicate = "https://cyclonedx.org/bom"

func (p provenanceInput) statement() ([]byte, error) {
	deps := []resolvedDep{}
	if p.Source.Repository != "" && p.Source.Commit != "" {
		deps = append(deps, resolvedDep{URI: "git+" + p.Source.Repository, Name: "source",
			Digest: map[string]string{"gitCommit": p.Source.Commit}})
	}
	for _, l := range p.Source.Libraries {
		if l.Commit == "" {
			continue
		}
		deps = append(deps, resolvedDep{URI: "git+" + l.Repository, Name: "library:" + l.Name,
			Digest: map[string]string{"gitCommit": l.Commit}})
	}
	deps = append(deps, p.BaseImages...)
	builders := slices.Clone(p.Builders)
	stmt := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       p.Subjects,
		"predicateType": SLSAProvenanceV1,
		"predicate": map[string]any{
			"buildDefinition": map[string]any{
				"buildType": BuildType,
				"externalParameters": map[string]any{
					"source":   map[string]any{"repository": p.Source.Repository, "ref": p.Source.Ref, "commit": p.Source.Commit},
					"trigger":  map[string]any{"event": p.Source.Event},
					"pipeline": map[string]any{"project": p.Source.Project, "name": p.Source.Pipeline, "step": p.Source.Step},
					"image":    p.Image,
				},
				"internalParameters":   map[string]any{"builders": builders},
				"resolvedDependencies": deps,
			},
			"runDetails": map[string]any{
				"builder": map[string]any{"id": p.BuilderID, "version": map[string]string{"launchway": p.Version}},
				"metadata": map[string]any{
					"invocationId": p.InvocationID,
					"startedOn":    p.Started.UTC().Format(time.RFC3339),
					"finishedOn":   p.Finished.UTC().Format(time.RFC3339),
				},
			},
		},
	}
	return json.MarshalIndent(stmt, "", "  ")
}

// baseImages reads the images a build was made from out of the builder's
// provenance (its resolvedDependencies or materials that are images).
func baseImages(prov map[string]json.RawMessage) []resolvedDep {
	seen := map[string]bool{}
	var out []resolvedDep
	for _, raw := range prov {
		var st struct {
			Predicate struct {
				BuildDefinition struct {
					ResolvedDependencies []resolvedDep `json:"resolvedDependencies"`
				} `json:"buildDefinition"`
				Materials []resolvedDep `json:"materials"`
			} `json:"predicate"`
		}
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		for _, d := range append(st.Predicate.BuildDefinition.ResolvedDependencies, st.Predicate.Materials...) {
			if len(d.URI) < 11 || d.URI[:11] != "pkg:docker/" || seen[d.URI] {
				continue
			}
			seen[d.URI] = true
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b resolvedDep) int {
		if a.URI < b.URI {
			return -1
		}
		if a.URI > b.URI {
			return 1
		}
		return 0
	})
	return out
}
