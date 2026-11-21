// Package preview runs the whole read-only half of the updater on one
// repository tree: read the repository's own settings, discover dependencies,
// ask the version sources and OSV, resolve and plan. It changes nothing; the
// dependency view, the settings page's "preview what would be opened", the
// command line and the scheduled run are all this one function.
package preview

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/ecosystems"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
)

// Input of Run.
type Input struct {
	// FS is the default branch's tree. The repository's .launchway/updates.yaml
	// is read from it, and only from it.
	FS fs.FS
	// Levels are the settings of the administrator levels above the
	// repository (instance, organization, project, repository settings), broad
	// first. The defaults come first and the repository's file last.
	Levels  []plan.Layer
	Sources map[string]source.Source
	// OSV answers vulnerability questions (nil: none are asked).
	OSV osv.Index
	// Open are the update pull requests that are open now.
	Open []plan.Open
	// Closed are what people closed or asked to ignore (resolver keys).
	Closed    map[string]bool
	Now       time.Time
	LastRun   time.Time
	Immediate bool
}

// Report is what the updater found and would do.
type Report struct {
	Policy    plan.Policy      `json:"policy"`
	Files     []string         `json:"files"`
	Notes     []depupdate.Note `json:"notes,omitempty"`
	Errors    []depupdate.Note `json:"errors,omitempty"`
	Updates   []plan.Update    `json:"updates"`
	Decisions []plan.Decision  `json:"decisions"`
	Vulns     []osv.Match      `json:"vulnerabilities,omitempty"`
	VulnError string           `json:"vulnerability_error,omitempty"`
	Coming    []string         `json:"coming"`
	FileError string           `json:"settings_file_error,omitempty"`
}

// Run does the work. A broken .launchway/updates.yaml does not stop it: the
// report says what is wrong and the settings of the levels apply.
func Run(ctx context.Context, in Input) (Report, error) {
	var rep Report
	layers := append([]plan.Layer{{Level: "defaults", Partial: plan.Defaults()}}, in.Levels...)
	if b, err := fs.ReadFile(in.FS, plan.FilePath); err == nil {
		if l, err := plan.ParseFile(b); err != nil {
			rep.FileError = err.Error()
		} else {
			layers = append(layers, l)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		rep.FileError = err.Error()
	}
	pol, err := plan.Merge(layers...)
	if err != nil && rep.FileError != "" {
		return rep, err
	}
	if err != nil { // the file made the policy invalid: report it, keep the levels
		rep.FileError = err.Error()
		pol, err = plan.Merge(layers[:len(layers)-1]...)
		if err != nil {
			return rep, err
		}
	}
	rep.Policy, rep.Coming = pol, depupdate.Coming()
	disc, err := depupdate.Discover(in.FS, ecosystems.Parsers(pol.ImageFiles), depupdate.Limits{})
	if err != nil {
		return rep, err
	}
	var deps []depupdate.Dep
	for _, d := range disc.Deps {
		if e, ok := pol.Ecosystems[d.Ecosystem]; ok && e.Enabled && inDirs(e.Directories, d.File) && !plan.AnyGlob(pol.Exclude, d.File) {
			deps = append(deps, d)
		}
	}
	rep.Files, rep.Notes, rep.Errors = disc.Files, disc.Notes, disc.Errors
	if in.OSV != nil {
		if rep.Vulns, err = osv.Check(ctx, in.OSV, deps); err != nil {
			rep.VulnError = err.Error()
		}
	}
	r := &plan.Resolver{Sources: in.Sources, Policy: pol, Now: func() time.Time { return in.Now }, Closed: in.Closed}
	rep.Updates = r.Resolve(ctx, deps, rep.Vulns)
	sort.SliceStable(rep.Updates, func(i, j int) bool {
		a, b := rep.Updates[i], rep.Updates[j]
		if a.Dep.File != b.Dep.File {
			return a.Dep.File < b.Dep.File
		}
		return a.Dep.Line < b.Dep.Line
	})
	rep.Decisions = plan.Plan(plan.Input{Updates: rep.Updates, Policy: pol, Open: in.Open, Now: in.Now, LastRun: in.LastRun, Immediate: in.Immediate})
	return rep, nil
}

func inDirs(dirs []string, file string) bool {
	if len(dirs) == 0 {
		return true
	}
	for _, d := range dirs {
		if d == "." || d == "/" || d == "" {
			return true
		}
		if len(file) > len(d) && file[:len(d)] == d && file[len(d)] == '/' {
			return true
		}
	}
	return false
}
