package pipeline

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/deploy/edit"
)

// Image steps (kind: image) build a container image from a Dockerfile in
// the workspace, and scan, sign and push it. They run no script: the
// runner does the work, with a BuildKit daemon its operator runs and the
// registries its configuration names (see internal/imagebuild). The step
// says what to build and what to call it; where the registries are and
// who may push where is the runner's business.
//
// Why a kind of its own rather than a build: block on a script step: the
// two run in different places with different credentials. A script runs
// in the step's container with the step's secrets; an image build runs in
// the builder, with registry credentials no script may see. A step that
// did both would blur exactly the line that keeps those credentials out of
// user code, and its outputs (digest, image) would mix with the script's.
const KindImage = "image"

// SchemaImages is the server schema that brought image steps and deploy
// changes that pin a digest. A pipeline that uses them carries it as
// Requires.
const SchemaImages = 15

// Defaults and limits of image steps.
const (
	DefaultImageTimeout = time.Hour
	MaxImageTags        = 20
	MaxImagePlatforms   = 8
	MaxBuildArgs        = 50
	MaxBuildSecrets     = 20
)

// ImageOutputs are the outputs an image step sets.
var ImageOutputs = []string{"digest", "image", "tags", "pushed"}

// ImageBuild is what an image step builds and where it goes.
type ImageBuild struct {
	Context   string            `json:"context"`          // relative to the workspace; default .
	File      string            `json:"file"`             // the Dockerfile, relative to the workspace; default <context>/Dockerfile
	Target    string            `json:"target,omitempty"` // a stage of the Dockerfile
	Platforms []string          `json:"platforms,omitempty"`
	BuildArgs map[string]*Value `json:"build_args,omitempty"`
	Tags      []ImageTag        `json:"tags"`
	// Push says whether to push; nil means yes. Pull-request and draft runs
	// never push, whatever it says.
	Push       *bool       `json:"push,omitempty"`
	Cache      *ImageRepo  `json:"cache,omitempty"`
	SBOM       bool        `json:"sbom,omitempty"`
	Provenance bool        `json:"provenance"`
	Sign       *ImageSign  `json:"sign,omitempty"`
	Scan       *ImageScan  `json:"scan,omitempty"`
	pushLine   int         // where push was written
	tagLines   map[int]int // line of each tag
}

// Pushes reports whether the step asks to push (before the rule for pull
// requests and drafts).
func (b *ImageBuild) Pushes() bool { return b.Push == nil || *b.Push }

// ImageTag is one name to push the image under: a registry the runner's
// configuration names, a repository in it, and a tag.
type ImageTag struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        *Value `json:"tag"`
}

// ImageRepo is a repository of a registry the runner names.
type ImageRepo struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
}

// ImageSign says how to sign. Runner signs with the runner's own key, which
// no step can read; Key with a key from a secret store, which steps of
// projects the store allows can read too.
type ImageSign struct {
	Runner   bool       `json:"runner,omitempty"`
	Key      *SecretRef `json:"key,omitempty"`
	Password *SecretRef `json:"password,omitempty"`
}

// ImageScan is the vulnerability scan's policy.
type ImageScan struct {
	// FailOn is critical, high, medium or low: with mode enforce, a finding
	// that bad or worse fails the step before anything is pushed.
	FailOn string `json:"fail_on,omitempty"`
	Mode   string `json:"mode"` // report (the default) or enforce
}

// IsImage reports whether the step builds an image.
func (s *Step) IsImage() bool { return s.Kind == KindImage }

// Image step files, relative to the runner's output directory for the
// step (not the workspace): what the step keeps.
const (
	imageSBOMGlob     = "sbom/*.cdx.json"
	imageFindingsGlob = "security/*.sarif"
	imageProvenance   = "provenance"
	// ProvenanceArtifact is the artifact an image step keeps its provenance
	// in: Launchway's statement and the builder's.
	ProvenanceArtifact = "provenance"
)

// HasImageSteps reports whether the pipeline builds images.
func (p *Pipeline) HasImageSteps() bool {
	for _, s := range p.Steps {
		if s.IsImage() {
			return true
		}
	}
	return false
}

// UsesSchema15 reports whether the pipeline uses what servers older than
// schema 15 do not know: image steps, and deploy changes that pin an image
// by digest.
func (p *Pipeline) UsesSchema15() bool {
	for _, s := range p.Steps {
		if s.IsImage() {
			return true
		}
		if s.ArgoCD != nil && s.ArgoCD.WriteBack != nil {
			for _, c := range s.ArgoCD.WriteBack.Changes {
				if c.Digest != nil {
					return true
				}
			}
		}
	}
	return false
}

var (
	imageKeys        = []string{"kind", "description", "needs", "when", "timeout", "retry", "fetch", "context", "file", "target", "platforms", "build_args", "secrets", "tags", "push", "cache", "sbom", "provenance", "sign", "scan"}
	imageTagKeys     = []string{"registry", "repository", "tag"}
	imageRepoKeys    = []string{"registry", "repository"}
	imageSecretKeys  = []string{"id", "secret"}
	imageSignKeys    = []string{"key", "password", "keyless"}
	imageScanKeys    = []string{"fail_on", "mode"}
	platformRe       = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)
	buildSecretIDRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
	registryRefRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	stageRe          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	severitiesFailOn = []string{"critical", "high", "medium", "low"}
)

// parseImageStep reads a step of kind image.
func (ps *parser) parseImageStep(k, v *yaml.Node, at string) *Step {
	s := &Step{ID: k.Value, Line: k.Line, Kind: KindImage}
	b := &ImageBuild{Context: ".", Provenance: true, tagLines: map[int]int{}}
	s.ImageBuild = b
	var timeoutNode, fileNode, tagsNode *yaml.Node
	var timeout time.Duration
	ps.cur = s
	defer func() { ps.cur = nil }()
	if v.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(v.Content); i += 2 {
			key := v.Content[i]
			switch {
			case contains(imageKeys, key.Value):
			case key.Value == "if" && ps.tmpl != nil:
			case key.Value == "for_each" || key.Value == "exclude" || key.Value == "max_parallel" || key.Value == "fail_fast":
				ps.add(key, at+"."+key.Value, "an image step cannot be repeated by for_each yet; write one image step for each image")
			case key.Value == "run" || key.Value == "image" || key.Value == "container" || key.Value == "env":
				ps.add(key, at+"."+key.Value, "an image step runs no script: the runner builds the Dockerfile named by file (and context) "+
					"with its builder; put commands in the Dockerfile, or in a step this one needs")
			case key.Value == "reports" || key.Value == "artifacts":
				ps.add(key, at+"."+key.Value, "an image step keeps its own reports: sbom: true keeps the SBOM, scan: keeps the vulnerabilities "+
					"found, and the provenance is kept as the artifact %q", ProvenanceArtifact)
			case key.Value == "uses" || key.Value == "with":
				ps.add(key, at+"."+key.Value, "an image step does not use a template")
			default:
				ps.add(key, at+"."+key.Value, "%q is not a key of image steps; expected one of: %s", key.Value, strings.Join(imageKeys, ", "))
			}
		}
	}
	known := imageKeys
	if ps.tmpl != nil {
		known = append(append([]string{}, imageKeys...), "if")
	}
	ps.eachKey(v, at, known, func(key, n *yaml.Node) {
		where := at + "." + key.Value
		switch key.Value {
		case "if":
			s.ifTest = ps.parseIf(n, where)
		case "kind":
		case "description":
			s.Description = strings.TrimSpace(ps.str(n, where))
			switch {
			case strings.ContainsAny(s.Description, "\r\n"):
				ps.add(n, where, "a description is one line; put longer notes in a comment")
			case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
				ps.add(n, where, "the description is longer than %d characters", MaxDescriptionLen)
			}
		case "needs":
			s.Needs = ps.strList(n, where)
			s.NeedsLine = n.Line
		case "when":
			s.When = ps.parseWhen(n, where)
		case "timeout":
			d, err := time.ParseDuration(ps.scalar(n, where))
			if err != nil {
				ps.add(n, where, "write a duration such as 30m or 1h")
			} else {
				timeoutNode, timeout = n, d
				s.timeoutSet = true
			}
		case "retry":
			s.Retry = ps.int(n, where)
			s.retrySet = true
			if s.Retry < 0 || s.Retry > MaxRetry {
				ps.add(n, where, "retry must be between 0 and %d", MaxRetry)
			}
		case "fetch":
			s.Fetch = ps.parseFetch(n, where)
		case "context":
			b.Context = ps.scalar(n, where)
			if b.Context != "." && !cleanRelPath(b.Context) {
				ps.add(n, where, "%q must be a directory relative to the workspace, without .. or a leading /", b.Context)
			}
		case "file":
			fileNode = n
			b.File = ps.scalar(n, where)
		case "target":
			b.Target = ps.scalar(n, where)
			if b.Target != "" && !stageRe.MatchString(b.Target) {
				ps.add(n, where, "%q is not a stage name", b.Target)
			}
		case "platforms":
			b.Platforms = ps.valueList(n, where)
			if len(b.Platforms) > MaxImagePlatforms {
				ps.add(n, where, "at most %d platforms", MaxImagePlatforms)
			}
			seen := map[string]bool{}
			for _, p := range b.Platforms {
				switch {
				case !platformRe.MatchString(p):
					ps.add(n, where, "%q is not a platform; write os/arch, such as linux/amd64 or linux/arm64", p)
				case seen[p]:
					ps.add(n, where, "%s is listed twice", p)
				}
				seen[p] = true
			}
		case "build_args":
			b.BuildArgs = ps.parseBuildArgs(n, where)
		case "secrets":
			s.Secrets = ps.parseBuildSecrets(n, where)
		case "tags":
			tagsNode = n
			b.Tags = ps.parseImageTags(n, where, b)
		case "push":
			p := ps.bool(n, where)
			b.Push, b.pushLine = &p, n.Line
		case "cache":
			b.Cache = ps.parseImageRepo(n, where, "cache: { registry: harbor, repository: team/cache/shop }")
		case "sbom":
			b.SBOM = ps.bool(n, where)
		case "provenance":
			b.Provenance = ps.bool(n, where)
		case "sign":
			b.Sign = ps.parseImageSign(n, where)
		case "scan":
			b.Scan = ps.parseImageScan(n, where)
		}
	})
	if v.Kind != yaml.MappingNode {
		return s
	}
	switch {
	case b.File == "":
		b.File = "Dockerfile"
		if b.Context != "." {
			b.File = b.Context + "/Dockerfile"
		}
	case !cleanRelPath(b.File):
		ps.add(fileNode, at+".file", "%q must be a path relative to the workspace, without .. or a leading /", b.File)
	}
	if tagsNode == nil && b.Pushes() {
		ps.add(k, at+".tags", "say what to call the image: tags: [{ registry: harbor, repository: team/app, tag: { run: short_sha } }], "+
			"or push: false to only build it")
	}
	if b.Cache != nil && b.Cache.Registry == "" {
		b.Cache = nil
	}
	s.Timeout = Duration(DefaultImageTimeout)
	if timeoutNode != nil {
		if timeout < time.Minute || timeout > MaxTimeout {
			ps.add(timeoutNode, at+".timeout", "timeout must be between 1m and %s", Duration(MaxTimeout).Short())
		} else {
			s.Timeout = Duration(timeout)
		}
	}
	// What the step keeps, as for any step: the runner writes these files
	// into a directory of its own, not the workspace.
	if b.SBOM || b.Scan != nil {
		s.Reports = &Reports{Tests: []string{}}
		if b.SBOM {
			s.Reports.SBOM = []string{imageSBOMGlob}
		}
		if b.Scan != nil {
			s.Reports.Findings = []string{imageFindingsGlob}
		}
	}
	if b.Provenance {
		s.Artifacts = []*Artifact{{Name: ProvenanceArtifact, Paths: []string{imageProvenance}, When: ArtifactAlways, Line: s.Line}}
	}
	return s
}

// imageValue reads a value of an image step: as for deploy steps, and in a
// template also a parameter.
func (ps *parser) imageValue(n *yaml.Node, at string) *Value {
	if n.Kind == yaml.MappingNode && (hasKey(n, "param") || hasKey(n, "each")) {
		return &Value{Literal: ps.scalar(n, at)}
	}
	if n.Kind == yaml.MappingNode && hasKey(n, "input") && ps.tmpl != nil {
		ps.add(n, at, "a template cannot read run inputs; take the value as a parameter of this template")
		return &Value{}
	}
	return ps.value(n, at)
}

func (ps *parser) parseBuildArgs(n *yaml.Node, at string) map[string]*Value {
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "build_args is a mapping, for example: build_args: { VERSION: { run: short_sha } }")
		return nil
	}
	out := map[string]*Value{}
	if len(n.Content)/2 > MaxBuildArgs {
		ps.add(n, at, "at most %d build arguments", MaxBuildArgs)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		switch {
		case !envKeyRe.MatchString(k.Value):
			ps.add(k, where, "%q is not a valid build argument name", k.Value)
			continue
		case out[k.Value] != nil:
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		}
		out[k.Value] = ps.imageValue(v, where)
	}
	return out
}

// parseBuildSecrets reads build secrets, a list of { id, secret }. They
// are kept as the step's secrets, by ID, so the rules for secrets (pull
// requests and drafts may not read them, unless allowed) hold for them.
func (ps *parser) parseBuildSecrets(n *yaml.Node, at string) map[string]SecretRef {
	out := map[string]SecretRef{}
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "secrets is a list, for example: secrets: [{ id: npm_token, secret: { store: vault, key: ci/npm, field: token } }]")
		return out
	}
	if len(n.Content) > MaxBuildSecrets {
		ps.add(n, at, "at most %d build secrets", MaxBuildSecrets)
	}
	for i, item := range n.Content {
		where := fmt.Sprintf("%s[%d]", at, i)
		var id string
		var ref SecretRef
		var refNode *yaml.Node
		ps.eachKey(item, where, imageSecretKeys, func(k, v *yaml.Node) {
			switch k.Value {
			case "id":
				id = ps.str(v, where+".id")
			case "secret":
				refNode = v
				ps.eachKey(v, where+".secret", secretKeys, func(k2, v2 *yaml.Node) {
					switch k2.Value {
					case "store":
						ref.Store = ps.str(v2, where+".secret.store")
					case "key":
						ref.Key = ps.str(v2, where+".secret.key")
					case "field":
						ref.Field = ps.str(v2, where+".secret.field")
					}
				})
			}
		})
		if item.Kind != yaml.MappingNode {
			continue
		}
		switch _, dup := out[id]; {
		case !buildSecretIDRe.MatchString(id):
			ps.add(item, where+".id", "give the secret an id the Dockerfile uses (RUN --mount=type=secret,id=...): letters, digits, _, . and -")
			continue
		case dup:
			ps.add(item, where+".id", "secret %q is listed twice", id)
			continue
		case refNode == nil:
			ps.add(item, where+".secret", "say where the secret is kept: secret: { store: vault, key: ci/npm, field: token }")
			continue
		case !ValidStoreName(ref.Store):
			ps.add(refNode, where+".secret.store", "name the store, in lower case, for example: store: vault")
		case strings.TrimSpace(ref.Key) == "":
			ps.add(refNode, where+".secret.key", "a key is required")
		}
		out[id] = ref
	}
	return out
}

func (ps *parser) parseImageTags(n *yaml.Node, at string, b *ImageBuild) []ImageTag {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "tags is a list, for example: tags: [{ registry: harbor, repository: team/app, tag: { run: short_sha } }]")
		return nil
	}
	if len(n.Content) == 0 {
		ps.add(n, at, "list at least one tag, or write push: false to only build the image")
	}
	if len(n.Content) > MaxImageTags {
		ps.add(n, at, "at most %d tags", MaxImageTags)
	}
	var out []ImageTag
	for i, item := range n.Content {
		where := fmt.Sprintf("%s[%d]", at, i)
		var t ImageTag
		ps.eachKey(item, where, imageTagKeys, func(k, v *yaml.Node) {
			switch k.Value {
			case "registry":
				t.Registry = ps.scalar(v, where+".registry")
			case "repository":
				t.Repository = ps.scalar(v, where+".repository")
			case "tag":
				t.Tag = ps.imageValue(v, where+".tag")
			}
		})
		if item.Kind != yaml.MappingNode {
			continue
		}
		ps.checkImageRepo(item, where, t.Registry, t.Repository)
		switch {
		case t.Tag == nil:
			ps.add(item, where+".tag", "give the tag, for example tag: { run: short_sha } or tag: latest")
		case t.Tag.Literal != "":
			if err := edit.CheckTag(t.Tag.Literal); err != nil {
				ps.add(item, where+".tag", "%s", err)
			}
		}
		b.tagLines[len(out)] = item.Line
		out = append(out, t)
	}
	return out
}

func (ps *parser) parseImageRepo(n *yaml.Node, at, example string) *ImageRepo {
	r := &ImageRepo{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "write %s", example)
		return r
	}
	ps.eachKey(n, at, imageRepoKeys, func(k, v *yaml.Node) {
		if k.Value == "registry" {
			r.Registry = ps.scalar(v, at+".registry")
		} else {
			r.Repository = ps.scalar(v, at+".repository")
		}
	})
	ps.checkImageRepo(n, at, r.Registry, r.Repository)
	return r
}

func (ps *parser) checkImageRepo(n *yaml.Node, at, registry, repository string) {
	switch {
	case registry == "":
		ps.add(n, at+".registry", "name the registry, as the runner's configuration calls it, for example registry: harbor")
	case !registryRefRe.MatchString(registry) && !strings.HasPrefix(registry, "{"):
		ps.add(n, at+".registry", "%q is not a registry name: name one the runner's configuration lists, in lower case", registry)
	}
	switch {
	case repository == "":
		ps.add(n, at+".repository", "give the repository, without the registry, for example repository: team/app")
	case strings.Contains(strings.SplitN(repository, "/", 2)[0], "."), strings.Contains(strings.SplitN(repository, "/", 2)[0], ":"):
		ps.add(n, at+".repository", "%q names a registry host; give the repository only, the registry is set by registry:", repository)
	default:
		if err := edit.CheckImage(repository); err != nil {
			ps.add(n, at+".repository", "%s", strings.Replace(err.Error(), "image ", "repository ", 1))
		}
	}
}

func (ps *parser) parseImageSign(n *yaml.Node, at string) *ImageSign {
	if n.Kind == yaml.ScalarNode {
		if ps.bool(n, at) {
			return &ImageSign{Runner: true}
		}
		return nil
	}
	s := &ImageSign{}
	readRef := func(v *yaml.Node, where string) *SecretRef {
		ref := &SecretRef{}
		ps.eachKey(v, where, secretKeys, func(k, x *yaml.Node) {
			switch k.Value {
			case "store":
				ref.Store = ps.str(x, where+".store")
			case "key":
				ref.Key = ps.str(x, where+".key")
			case "field":
				ref.Field = ps.str(x, where+".field")
			}
		})
		if v.Kind == yaml.MappingNode && (!ValidStoreName(ref.Store) || strings.TrimSpace(ref.Key) == "") {
			ps.add(v, where, "write a secret reference: { store: vault, key: ci/cosign, field: key }")
		}
		return ref
	}
	ps.eachKey(n, at, imageSignKeys, func(k, v *yaml.Node) {
		switch k.Value {
		case "key":
			s.Key = readRef(v, at+".key")
		case "password":
			s.Password = readRef(v, at+".password")
		case "keyless":
			ps.add(k, at+".keyless", "keyless signing (a certificate for an OIDC identity, and a transparency log) is not supported yet; "+
				"sign with the runner's key (sign: true) or a key in a secret store (sign: { key: ... })")
		}
	})
	if n.Kind == yaml.MappingNode && s.Key == nil && !hasKey(n, "keyless") {
		ps.add(n, at+".key", "say which key: sign: true signs with the runner's own key; sign: { key: { store: vault, key: ci/cosign, field: key } } with yours")
	}
	if s.Password != nil && s.Key == nil {
		ps.add(n, at+".password", "a password goes with a key")
	}
	return s
}

func (ps *parser) parseImageScan(n *yaml.Node, at string) *ImageScan {
	if n.Kind == yaml.ScalarNode {
		if ps.bool(n, at) {
			return &ImageScan{Mode: "report"}
		}
		return nil
	}
	s := &ImageScan{Mode: "report"}
	var modeNode *yaml.Node
	ps.eachKey(n, at, imageScanKeys, func(k, v *yaml.Node) {
		switch k.Value {
		case "fail_on":
			s.FailOn = ps.str(v, at+".fail_on")
			if !slices.Contains(severitiesFailOn, s.FailOn) {
				ps.add(v, at+".fail_on", "write critical, high, medium or low")
			}
		case "mode":
			modeNode = v
			s.Mode = ps.str(v, at+".mode")
			if s.Mode != "report" && s.Mode != "enforce" {
				ps.add(v, at+".mode", "write report (record what is found) or enforce (fail the step, before pushing, on a finding at fail_on or worse)")
			}
		}
	})
	if s.Mode == "enforce" && s.FailOn == "" {
		ps.add(modeNode, at+".fail_on", "mode: enforce needs fail_on: the severity that fails the step")
	}
	return s
}

// imageValues lists an image step's values.
func imageValues(s *Step) []*Value {
	if s.ImageBuild == nil {
		return nil
	}
	var out []*Value
	for _, k := range slices.Sorted(maps.Keys(s.ImageBuild.BuildArgs)) {
		out = append(out, s.ImageBuild.BuildArgs[k])
	}
	for _, t := range s.ImageBuild.Tags {
		if t.Tag != nil {
			out = append(out, t.Tag)
		}
	}
	return out
}

// checkImages validates what image steps say about other steps and inputs,
// once every step is known: the outputs they use must come from steps they
// need, and inputs must exist and be single values.
func (ps *parser) checkImages(p *Pipeline) {
	for _, s := range p.Steps {
		if !s.IsImage() {
			continue
		}
		at := "steps." + s.ID
		for _, v := range imageValues(s) {
			switch {
			case v.Output != nil && p.Step(v.Output.Step) == nil && p.Group(v.Output.Step) == nil:
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at,
					Message: fmt.Sprintf("%s refers to %s.%s, but there is no step %s", s.ID, v.Output.Step, v.Output.Key, v.Output.Step)})
			case v.Output != nil && !p.Upstream(s.ID)[v.Output.Step] && !contains(s.NeedsGroups, v.Output.Step):
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at,
					Message: fmt.Sprintf("%s refers to %s.%s, but %s is not in this step's needs", s.ID, v.Output.Step, v.Output.Key, v.Output.Step)})
			case v.Input != "":
				in := p.Input(v.Input)
				switch {
				case in == nil:
					ps.problems = append(ps.problems, Problem{Line: v.inputLine, Path: at, Message: fmt.Sprintf("%s: there is no input called %q", s.ID, clipText(v.Input, 60))})
				case in.Type == InputList || in.Type == InputBoolean:
					ps.problems = append(ps.problems, Problem{Line: v.inputLine, Path: at, Message: fmt.Sprintf(
						"%s: %s is a %s; an image step's values take a string, choice or number input", s.ID, in.Name, in.Type)})
				}
			}
		}
	}
	// Steps that read an image step's outputs may read only the ones it sets.
	for _, s := range p.Steps {
		var refs []*Value
		refs = append(refs, deployValues(s)...)
		refs = append(refs, imageValues(s)...)
		for _, v := range refs {
			if v.Output == nil {
				continue
			}
			if src := p.Step(v.Output.Step); src != nil && src.IsImage() && !contains(ImageOutputs, v.Output.Key) {
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID,
					Message: fmt.Sprintf("%s refers to %s.%s, but an image step's outputs are %s", s.ID, v.Output.Step, v.Output.Key, strings.Join(ImageOutputs, ", "))})
			}
		}
	}
}

// bindImage puts a run's input values in an image step's values. It
// returns a copy; s is not changed.
func bindImage(s *Step, values map[string]string) *Step {
	need := false
	for _, v := range imageValues(s) {
		need = need || v.Input != ""
	}
	if !need {
		return s
	}
	c := *s
	b := *s.ImageBuild
	lit := func(v *Value) *Value {
		if v == nil || v.Input == "" {
			return v
		}
		return &Value{Literal: values[v.Input]}
	}
	b.BuildArgs = map[string]*Value{}
	for k, v := range s.ImageBuild.BuildArgs {
		b.BuildArgs[k] = lit(v)
	}
	b.Tags = make([]ImageTag, len(s.ImageBuild.Tags))
	for i, t := range s.ImageBuild.Tags {
		t.Tag = lit(t.Tag)
		b.Tags[i] = t
	}
	c.ImageBuild = &b
	return &c
}

// imageOf gives a template's image step's values the run's step IDs: an
// output of a step of the same template is that step's, expanded.
func (x *expander) imageOf(sc *scope, s *Step) *ImageBuild {
	if s.ImageBuild == nil {
		return nil
	}
	b := *s.ImageBuild
	local := func(v *Value) *Value {
		if v == nil || v.Output == nil || sc.byID[v.Output.Step] == nil {
			return v
		}
		c := *v
		c.Output = &OutputRef{Step: sc.prefix + v.Output.Step, Key: v.Output.Key}
		return &c
	}
	if len(s.ImageBuild.BuildArgs) > 0 {
		b.BuildArgs = make(map[string]*Value, len(s.ImageBuild.BuildArgs))
		for k, v := range s.ImageBuild.BuildArgs {
			b.BuildArgs[k] = local(v)
		}
	}
	b.Tags = make([]ImageTag, len(s.ImageBuild.Tags))
	for i, t := range s.ImageBuild.Tags {
		t.Tag = local(t.Tag)
		b.Tags[i] = t
	}
	return &b
}
