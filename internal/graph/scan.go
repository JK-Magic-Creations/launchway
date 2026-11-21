package graph

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/container"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// What the repository sources read out of a tree, shared by the sources that
// need the same facts (the container source and the requirements source).

// NormalizeImage writes an image name in full: a host and a path, no tag.
func NormalizeImage(name string) string {
	name = strings.TrimSuffix(name, "/")
	first, rest, ok := strings.Cut(name, "/")
	if !ok {
		return "docker.io/library/" + name
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		if first == "index.docker.io" || first == "registry-1.docker.io" {
			first = "docker.io"
		}
		if first == "docker.io" && !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
		return first + "/" + rest
	}
	return "docker.io/" + name
}

// ImageName is the name of an image reference with its tag and digest taken
// off, written in full.
func ImageName(ref string) string { return NormalizeImage(container.ParseRef(ref).Repo) }

// Stage is one FROM of a Containerfile.
type Stage struct {
	Line   int
	Name   string // AS name, or ""
	Index  int
	Final  bool
	Base   string // normalised image, or "" when the base is an earlier stage or scratch
	Tag    string
	Digest string
	Parent string // the earlier stage this one starts from, if any
}

// Containerfile is what one Containerfile says.
type Containerfile struct {
	Path   string
	Stages []Stage
	Ports  []int
	Copies []string // stages copied from
}

var (
	cfFromRe   = regexp.MustCompile(`(?i)^FROM\s+(?:--[a-z-]+=\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?`)
	cfExposeRe = regexp.MustCompile(`(?i)^EXPOSE\s+(.*)$`)
)

func isContainerfile(p string) bool {
	b := strings.ToLower(path.Base(p))
	return b == "dockerfile" || b == "containerfile" || strings.HasPrefix(b, "dockerfile.") || strings.HasSuffix(b, ".dockerfile") ||
		strings.HasPrefix(b, "containerfile.") || strings.HasSuffix(b, ".containerfile")
}

// ScanContainerfiles reads every Containerfile of the tree: its stages with
// their bases (through the updater's own reading of FROM lines) and the
// ports it exposes.
func ScanContainerfiles(tree fs.FS) []Containerfile {
	var out []Containerfile
	_ = fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isContainerfile(p) {
			return nil
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return nil
		}
		cf := Containerfile{Path: p}
		deps, _, _ := container.Parser{}.Parse(depupdate.File{Path: p, Data: data})
		byLine := map[int]depupdate.Dep{}
		for _, dp := range deps {
			byLine[dp.Line] = dp
		}
		stageNames := map[string]bool{}
		lines := strings.Split(string(data), "\n")
		for i := 0; i < len(lines); i++ {
			l := strings.TrimSpace(lines[i])
			for strings.HasSuffix(l, "\\") && i+1 < len(lines) {
				i++
				l = strings.TrimSpace(strings.TrimSuffix(l, "\\")) + " " + strings.TrimSpace(lines[i])
			}
			if m := cfFromRe.FindStringSubmatch(l); m != nil {
				st := Stage{Line: i + 1, Name: m[2], Index: len(cf.Stages)}
				if stageNames[m[1]] {
					st.Parent = m[1]
				} else if dp, ok := byLine[i+1]; ok {
					st.Base, st.Tag, st.Digest = NormalizeImage(dp.Name), dp.Constraint, dp.Digest
				} else {
					// via an ARG: the updater reports it at the ARG's line
					for _, dp := range deps {
						if dp.Line < i+1 && dp.Name != "" && !used(cf, dp) {
							st.Base, st.Tag, st.Digest = NormalizeImage(dp.Name), dp.Constraint, dp.Digest
						}
					}
				}
				if m[2] != "" {
					stageNames[m[2]] = true
				}
				cf.Stages = append(cf.Stages, st)
				continue
			}
			if m := cfExposeRe.FindStringSubmatch(l); m != nil {
				for _, f := range strings.Fields(m[1]) {
					f = strings.SplitN(f, "/", 2)[0]
					if n, err := strconv.Atoi(f); err == nil && n > 0 && n < 65536 {
						cf.Ports = append(cf.Ports, n)
					}
				}
			}
		}
		if n := len(cf.Stages); n > 0 {
			cf.Stages[n-1].Final = true
		}
		sort.Ints(cf.Ports)
		out = append(out, cf)
		return nil
	})
	return out
}

func used(cf Containerfile, dp depupdate.Dep) bool {
	for _, s := range cf.Stages {
		if s.Base == NormalizeImage(dp.Name) && s.Tag == dp.Constraint {
			return true
		}
	}
	return false
}

// ImageStep is one image step of a repository's pipeline.
type ImageStep struct {
	Step      string
	Context   string
	File      string
	Platforms []string
	Targets   []string // image names pushed, as "registry/repository"
}

// PipelineFile is the repository's pipeline as the graph needs it.
type PipelineFile struct {
	Name   string
	Images []ImageStep
}

// PipelineFilePath is where a repository keeps its pipeline.
const PipelineFilePath = ".launchway/pipeline.yaml"

// ScanPipeline reads the repository's pipeline file for its image steps. A
// file that does not parse yields nothing; the pipeline's own checks are what
// say why.
func ScanPipeline(tree fs.FS) (PipelineFile, bool) {
	data, err := fs.ReadFile(tree, PipelineFilePath)
	if err != nil {
		return PipelineFile{}, false
	}
	p, err := pipeline.Parse(data)
	if err != nil || p == nil {
		return PipelineFile{}, false
	}
	out := PipelineFile{Name: p.Name}
	for _, st := range p.Steps {
		b := st.ImageBuild
		if st.Kind != pipeline.KindImage || b == nil {
			continue
		}
		is := ImageStep{Step: st.ID, Context: cleanPath(b.Context), File: cleanPath(b.File), Platforms: append([]string{}, b.Platforms...)}
		if is.Context == "" {
			is.Context = "."
		}
		if is.File == "" {
			is.File = path.Join(is.Context, "Dockerfile")
		}
		for _, t := range b.Tags {
			is.Targets = append(is.Targets, t.Registry+"/"+t.Repository)
		}
		out.Images = append(out.Images, is)
	}
	return out, true
}

// Arch is the architecture of a platform such as linux/arm64/v8.
func Arch(platform string) string {
	f := strings.Split(platform, "/")
	if len(f) >= 2 {
		return f[1]
	}
	return platform
}
