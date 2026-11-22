package importer

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Limits on a request, whatever its source: the API, the CLI and the wizard all
// go through Import.
const (
	MaxFiles      = 200
	MaxTotalBytes = 4 << 20
)

// Request asks for an import.
type Request struct {
	// Format is a format id, or "" or "auto" to detect it from the file names.
	Format string
	Files  []File
	// Root narrows the import to one file (its path); empty imports every root file found.
	Root    string
	Options Options
	Limits  Limits
}

// Detected is a source file the importer recognises.
type Detected struct {
	Path   string `json:"path"`
	Format string `json:"format"`
}

// Result is what an import found.
type Result struct {
	Plans    []*Plan    `json:"plans"`
	Detected []Detected `json:"detected"`
	Notes    []Finding  `json:"notes,omitempty"` // problems with the request itself
}

// ErrNothing is returned when no source file was found.
var ErrNothing = errors.New("no pipeline files of a known format were found")

// Detect lists the files of a repository that are pipeline definitions.
func Detect(files []File) []Detected {
	var out []Detected
	for _, f := range files {
		head := f.Content
		if len(head) > 2048 {
			head = head[:2048]
		}
		for _, fm := range formats {
			if fm.Detect(path.Clean(f.Path), head) {
				out = append(out, Detected{Path: f.Path, Format: fm.ID})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Import reads the files and proposes a pipeline for each root file.
func Import(req Request) (*Result, error) {
	if len(req.Files) > MaxFiles {
		return nil, fmt.Errorf("too many files (%d; at most %d)", len(req.Files), MaxFiles)
	}
	total := 0
	for _, f := range req.Files {
		total += len(f.Content)
	}
	if total > MaxTotalBytes {
		return nil, fmt.Errorf("the files are too large together (%d bytes; at most %d)", total, MaxTotalBytes)
	}
	lim := req.Limits
	if lim.MaxBytes == 0 {
		lim = DefaultLimits
	}
	all := map[string]File{}
	for _, f := range req.Files {
		f.Path = path.Clean(strings.TrimPrefix(f.Path, "./"))
		all[f.Path] = f
	}
	res := &Result{Detected: Detect(req.Files)}
	var roots []Detected
	for _, d := range res.Detected {
		if req.Root != "" && d.Path != req.Root {
			continue
		}
		if req.Format != "" && req.Format != "auto" && d.Format != req.Format {
			continue
		}
		roots = append(roots, d)
	}
	if req.Format != "" && req.Format != "auto" {
		fm := FormatByID(req.Format)
		if fm == nil {
			return nil, fmt.Errorf("unknown format %q; known: %s", req.Format, strings.Join(FormatIDs(), ", "))
		}
		// A file named by the caller is read in that format even if its name is not the usual one.
		if len(roots) == 0 {
			for _, f := range req.Files {
				if req.Root == "" || f.Path == req.Root {
					roots = append(roots, Detected{Path: f.Path, Format: req.Format})
				}
			}
		}
	}
	if len(roots) == 0 {
		return res, ErrNothing
	}
	for _, d := range roots {
		fm := FormatByID(d.Format)
		p := fm.Read(all[d.Path], all, lim)
		o := req.Options
		plan, err := Write(p, o)
		if err != nil {
			res.Plans = append(res.Plans, &Plan{Format: d.Format, Files: []string{d.Path}, Findings: append(p.Findings, Finding{Code: "write.error", Class: Manual, Message: err.Error(), Next: "Write this pipeline by hand, or fix the findings above", File: d.Path})})
			continue
		}
		res.Plans = append(res.Plans, plan)
	}
	return res, nil
}

// FormatIDs lists the format ids.
func FormatIDs() []string {
	var ids []string
	for _, f := range formats {
		ids = append(ids, f.ID)
	}
	return ids
}
