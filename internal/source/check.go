package source

import (
	"context"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RepositoryCheck is what looking at a repository before connecting it found.
type RepositoryCheck struct {
	OK bool `json:"ok"`
	// Problem says in plain words why the repository could not be read.
	Problem       string   `json:"problem,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Branches      []string `json:"branches"`
	// HasPipelineFile: the branch (the default branch if none was asked
	// for) has .launchway/pipeline.yaml.
	HasPipelineFile bool `json:"has_pipeline_file"`
	// PipelineFile is the path found, Commit the commit it was read at,
	// Valid whether the file passes the checks a run would make of it, and
	// Problems what does not, each with its file, line and the commit.
	PipelineFile string            `json:"pipeline_file,omitempty"`
	Commit       string            `json:"commit,omitempty"`
	Valid        bool              `json:"valid"`
	Problems     []PipelineProblem `json:"problems"`
}

// PipelineProblem is something wrong in the pipeline file, where it is.
type PipelineProblem struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Commit  string `json:"commit"`
	Message string `json:"message"`
}

// CheckRepository reads a repository the way the server would, with the
// server's own Git credentials, without keeping anything: its branches,
// its default branch and whether a branch has a pipeline file. A repository
// that cannot be read is reported in Problem, not as an error.
func CheckRepository(ctx context.Context, repo, branch string) RepositoryCheck {
	out := RepositoryCheck{Branches: []string{}, Problems: []PipelineProblem{}}
	repo = strings.TrimSpace(repo)
	switch {
	case repo == "":
		out.Problem = "give the repository's address"
		return out
	case strings.HasPrefix(repo, "-") || transportRE.MatchString(repo):
		out.Problem = "that is not a Git address; use an https:// or ssh address, or a path on this server"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	env := []string{"GIT_ALLOW_PROTOCOL=file:git:http:https:ssh"}
	text, err := gitRun(ctx, "", env, "", "ls-remote", "--symref", "--heads", "--", repo)
	if err != nil {
		out.Problem = explainGit(err)
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "ref: refs/heads/"):
			if name, _, ok := strings.Cut(strings.TrimPrefix(line, "ref: refs/heads/"), "\tHEAD"); ok {
				out.DefaultBranch = name
			}
		default:
			if _, ref, ok := strings.Cut(line, "\t"); ok && strings.HasPrefix(ref, "refs/heads/") {
				out.Branches = append(out.Branches, strings.TrimPrefix(ref, "refs/heads/"))
			}
		}
	}
	sort.Strings(out.Branches)
	if len(out.Branches) == 0 {
		out.Problem = "the repository can be reached but has no branches yet; push a first commit"
		return out
	}
	if out.DefaultBranch == "" {
		out.DefaultBranch = out.Branches[0]
		for _, b := range out.Branches {
			if b == "main" || b == "master" {
				out.DefaultBranch = b
				break
			}
		}
	}
	want := branch
	if want == "" {
		want = out.DefaultBranch
	}
	found := false
	for _, b := range out.Branches {
		found = found || b == want
	}
	if !found {
		out.Problem = fmt.Sprintf("the repository has no branch %q; it has %s", want, strings.Join(clipList(out.Branches, 8), ", "))
		return out
	}
	out.OK = true
	path, text, sha := pipelineFileOn(ctx, repo, want, env)
	out.HasPipelineFile, out.PipelineFile, out.Commit = path != "", path, sha
	if path != "" {
		if _, err := pipeline.Parse([]byte(text)); err != nil {
			var ps pipeline.Problems
			if errors.As(err, &ps) {
				for _, p := range ps {
					file := path
					if p.File != "" {
						file = p.File
					}
					out.Problems = append(out.Problems, PipelineProblem{File: file, Line: p.Line, Column: p.Column, Commit: sha, Message: problemText(p)})
				}
			} else {
				out.Problems = append(out.Problems, PipelineProblem{File: path, Commit: sha, Message: err.Error()})
			}
		} else {
			out.Valid = true
		}
	}
	return out
}

func clipList(l []string, n int) []string {
	if len(l) <= n {
		return l
	}
	return append(append([]string{}, l[:n]...), "...")
}

// pipelineFileOn fetches the branch's tip into a temporary repository and
// reads the pipeline file: its path, its text and the commit. The path is ""
// if there is none or it cannot be told.
func pipelineFileOn(ctx context.Context, repo, branch string, env []string) (path, text, sha string) {
	dir, err := os.MkdirTemp("", "launchway-check-")
	if err != nil {
		return "", "", ""
	}
	defer os.RemoveAll(dir)
	if _, err := gitRun(ctx, dir, nil, "", "init", "-q"); err != nil {
		return "", "", ""
	}
	if _, err := gitRun(ctx, dir, env, "", "fetch", "-q", "--depth=1", "--filter=blob:none", "--", repo, "refs/heads/"+branch); err != nil {
		// A host that cannot filter: fetch the full tip.
		if _, err := gitRun(ctx, dir, env, "", "fetch", "-q", "--depth=1", "--", repo, "refs/heads/"+branch); err != nil {
			return "", "", ""
		}
	}
	head, _ := gitRun(ctx, dir, nil, "", "rev-parse", "FETCH_HEAD")
	sha = strings.TrimSpace(head)
	for _, p := range PipelinePaths {
		if out, err := gitRun(ctx, dir, nil, "", "show", "FETCH_HEAD:"+p); err == nil {
			return p, out, sha
		}
	}
	return "", "", sha
}

// explainGit turns what Git said into something a person can act on.
func explainGit(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "authentication failed"), strings.Contains(low, "could not read username"),
		strings.Contains(low, "permission denied"), strings.Contains(low, "terminal prompts disabled"):
		return "the server could not sign in to that repository; it uses the Git credentials of the account the server runs as (an SSH key or a credential helper), and they do not give access. Git said: " + clipMsg(msg)
	case strings.Contains(low, "not found"), strings.Contains(low, "does not appear to be a git repository"):
		return "no repository is there; check the address. Git said: " + clipMsg(msg)
	case strings.Contains(low, "could not resolve host"), strings.Contains(low, "connection refused"), strings.Contains(low, "timed out"):
		return "the server could not reach that host. Git said: " + clipMsg(msg)
	case errors.Is(err, context.DeadlineExceeded):
		return "reading the repository took too long"
	}
	return clipMsg(msg)
}

func clipMsg(s string) string {
	s = redact.URLs(s)
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// transportRE finds Git's helper transports (ext::, fd::), which run commands.
var transportRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

func problemText(p pipeline.Problem) string {
	if p.Path != "" {
		return p.Path + ": " + p.Message
	}
	return p.Message
}
