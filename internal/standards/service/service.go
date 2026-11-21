// Package service puts Standing orders to work on a server: it reads the
// settings of the levels a project is in and the repository's own file,
// makes the effective profile, and adds the Inspection to runs. The HTTP
// API, the command line and the MCP tools all use it, so each says the same
// thing.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/standards"
)

// Service is Standing orders on one server.
type Service struct {
	Eng *engine.Engine
	// Layout says which unit a project is in and what the levels are called.
	Layout func() (*settings.Layout, error)
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// maxFiles bounds the paths listed for detection.
const maxFiles = 200_000

// Words are the names of the levels as people read them here.
func (s *Service) Words() map[string]string {
	w := map[string]string{}
	for k, v := range standards.DefaultWords {
		w[k] = v
	}
	if s != nil && s.Layout != nil {
		if lay, err := s.Layout(); err == nil && lay != nil && lay.Levels.Installation.Singular != "" {
			w[standards.LevelInstallation] = lay.Levels.Installation.Singular
			w[standards.LevelOrganization] = lay.Levels.Organisation.Singular
			w[standards.LevelUnit] = lay.Levels.Unit.Singular
			w[standards.LevelProject] = lay.Levels.Project.Singular
		}
	}
	return w
}

// ScopeOf says which unit a project is in ("" for none).
func (s *Service) ScopeOf(project string) (unit string, err error) {
	if s.Layout == nil {
		return "", nil
	}
	lay, err := s.Layout()
	if err != nil || lay == nil {
		return "", err
	}
	return lay.ProjectUnit[project], nil
}

// Doc reads the settings stored for a level and scope; empty settings when
// none are stored. The version is 0 then.
func (s *Service) Doc(level, scope string) (standards.Settings, int, error) {
	d, err := s.Eng.StandingSettings(level, scope)
	if err != nil || d == nil {
		return standards.Settings{}, 0, err
	}
	var set standards.Settings
	if err := json.Unmarshal(d.Doc, &set); err != nil {
		return standards.Settings{}, d.Version, fmt.Errorf("the stored settings of %s %s cannot be read: %w", level, scope, err)
	}
	return set, d.Version, nil
}

// Levels returns the documents of the levels a project is in, widest first.
func (s *Service) Levels(project string) ([]standards.LevelDoc, error) {
	unit, err := s.ScopeOf(project)
	if err != nil {
		return nil, err
	}
	scopes := []struct{ level, scope string }{{standards.LevelInstallation, ""}, {standards.LevelOrganization, settings.DefaultOrg}}
	if unit != "" {
		scopes = append(scopes, struct{ level, scope string }{standards.LevelUnit, unit})
	}
	scopes = append(scopes, struct{ level, scope string }{standards.LevelProject, project})
	var out []standards.LevelDoc
	for _, sc := range scopes {
		set, _, err := s.Doc(sc.level, sc.scope)
		if err != nil {
			return nil, err
		}
		out = append(out, standards.LevelDoc{Level: sc.level, Scope: sc.scope, Settings: set})
	}
	return out, nil
}

// Files lists the repository's paths at a commit.
func Files(ctx context.Context, gitDir, sha string) ([]string, error) {
	out, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"ls-tree", "-r", "-z", "--name-only", sha}, MaxOutput: 32 << 20})
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			files = append(files, p)
			if len(files) >= maxFiles {
				break
			}
		}
	}
	return files, nil
}

// RepoFile reads the repository's own settings from the default branch. It
// returns nil when there is no file, and the problems found when there are.
func (s *Service) RepoFile(ctx context.Context, gitDir, defaultBranch string) (*standards.Settings, []string) {
	if gitDir == "" || defaultBranch == "" {
		return nil, nil
	}
	for _, ref := range []string{"refs/heads/" + defaultBranch, defaultBranch} {
		out, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"show", ref + ":" + standards.FilePath}, MaxOutput: standards.MaxFileBytes + 1})
		if err != nil {
			continue
		}
		set, problems := standards.ParseFile([]byte(out), s.now())
		return &set, problems
	}
	return nil, nil
}

// Effective is the profile of a project for the files it has.
func (s *Service) Effective(project string, files []string, file *standards.Settings, problems []string) (standards.Effective, error) {
	levels, err := s.Levels(project)
	if err != nil {
		return standards.Effective{}, err
	}
	return standards.Resolve(standards.ResolveInput{Levels: levels, File: file, FileProblems: problems, Files: files, Now: s.now(), Words: s.Words()}), nil
}

// Inspect is source.Inspector: it adds the Inspection of the effective
// Standing orders to the pipeline of a run being made.
func (s *Service) Inspect(ctx context.Context, req source.InspectRequest, p *pipeline.Pipeline) (*pipeline.Pipeline, string, error) {
	if !s.Eng.StandingsKnown() {
		return p, "", nil
	}
	files, err := Files(ctx, req.GitDir, req.SHA)
	if err != nil {
		return p, "", fmt.Errorf("the files at %s could not be listed: %w", short(req.SHA), err)
	}
	file, problems := s.RepoFile(ctx, req.GitDir, req.DefaultBranch)
	eff, err := s.Effective(req.Project, files, file, problems)
	if err != nil {
		return p, "", err
	}
	pol := standards.PolicyOf(eff, s.now())
	pol.DefaultBranchRun = req.OnDefaultBranch
	since := req.Since
	if !s.Eng.StandingInspected(req.Project) {
		since = "" // the first inspection reads every commit
	}
	if since == "" && s.Eng.StandingInspected(req.Project) && req.Event == pipeline.EventPullRequest && req.DefaultBranch != "" {
		if out, err := gitexec.Output(ctx, gitexec.Spec{GitDir: req.GitDir, Args: []string{"rev-parse", "--verify", "-q", "refs/heads/" + req.DefaultBranch}}); err == nil {
			since = strings.TrimSpace(out)
		}
	}
	q, inj, err := standards.Apply(p, standards.InjectInput{Effective: eff, Policy: pol, Since: since})
	if err != nil {
		return p, "", err
	}
	if inj == nil || len(inj.Steps) == 0 {
		return p, "", nil
	}
	note := fmt.Sprintf("Standing orders added an Inspection of %d checks (profile %s).", len(inj.Steps)-1, eff.ProfileVersion)
	return q, note, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Project is what the pages show of a project: the effective profile at the
// default branch's tip, and what was read to make it.
type Project struct {
	Effective standards.Effective `json:"effective"`
	Commit    string              `json:"commit,omitempty"`
	Branch    string              `json:"branch,omitempty"`
	// File says whether the repository has its own settings file, and what
	// is wrong with it.
	File         bool     `json:"file"`
	FileProblems []string `json:"file_problems,omitempty"`
	FileText     string   `json:"file_text,omitempty"`
	// Empty: the repository has no commit yet, so only what applies
	// everywhere is shown.
	Empty bool `json:"empty,omitempty"`
}

// ForProject makes the effective profile of a project from what its default
// branch holds now.
func (s *Service) ForProject(ctx context.Context, project, gitDir, branch string) (*Project, error) {
	out := &Project{Branch: branch}
	var files []string
	if gitDir != "" && branch != "" {
		sha, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"rev-parse", "--verify", "-q", "refs/heads/" + branch}})
		if err == nil && strings.TrimSpace(sha) != "" {
			out.Commit = strings.TrimSpace(sha)
			if files, err = Files(ctx, gitDir, out.Commit); err != nil {
				return nil, err
			}
		}
	}
	out.Empty = out.Commit == ""
	file, problems := s.RepoFile(ctx, gitDir, branch)
	out.File, out.FileProblems = file != nil, problems
	if file != nil {
		if text, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"show", "refs/heads/" + branch + ":" + standards.FilePath}, MaxOutput: standards.MaxFileBytes + 1}); err == nil {
			out.FileText = text
		}
	}
	eff, err := s.Effective(project, files, file, problems)
	if err != nil {
		return nil, err
	}
	out.Effective = eff
	return out, nil
}
