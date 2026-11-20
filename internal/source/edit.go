package source

// Editing pipelines. Git stays the only place a pipeline is kept: the editor
// reads the file from a branch, a draft run runs edited text without saving
// it (the text is kept with that run only), and saving commits the file to
// the repository as the person who saved it.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// PushSettings let people save pipelines to a project's repository. A
// project without them cannot be saved to. Credentials are whatever the
// server's Git uses to read the repository: the URL, or GIT_CONFIG_*
// settings in its environment.
type PushSettings struct {
	// BranchPrefix starts the name of every branch a save creates, as in
	// launchway/dana/20261114-093000. Default "launchway/".
	BranchPrefix string `json:"branch_prefix"`
	// AllowDirect lists the branches (path.Match patterns, where * does not
	// cross a /) that may be committed to directly instead of through a new
	// branch. Empty: none.
	AllowDirect []string `json:"allow_direct"`
}

// DefaultBranchPrefix is the BranchPrefix when none is set.
const DefaultBranchPrefix = "launchway/"

var prefixRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// check validates the settings and fills in the default prefix.
func (s *PushSettings) check() error {
	if s.BranchPrefix == "" {
		s.BranchPrefix = DefaultBranchPrefix
	}
	p := s.BranchPrefix
	if len(p) > 100 || !prefixRe.MatchString(p) || strings.Contains(p, "..") || strings.Contains(p, "//") ||
		strings.Contains(p, "/.") || strings.HasSuffix(p, ".") || strings.HasSuffix(p, ".lock") || strings.Contains(p, ".lock/") {
		return fmt.Errorf("push.branch_prefix %q is not usable in a branch name; use letters, digits, '-', '_', '.' and '/', such as launchway/", p)
	}
	for _, pat := range s.AllowDirect {
		if _, err := path.Match(pat, ""); err != nil || pat == "" {
			return fmt.Errorf("push.allow_direct: %q is not a valid pattern", pat)
		}
	}
	return nil
}

// directAllowed reports whether branch may be committed to directly.
func (s *PushSettings) directAllowed(branch string) bool {
	for _, pat := range s.AllowDirect {
		if ok, _ := path.Match(pat, branch); ok {
			return true
		}
	}
	return false
}

// Push returns a project's push settings, or nil if it cannot be saved to.
func (m *Manager) Push(name string) *PushSettings {
	if p := m.c().projects[name]; p != nil && p.Push != nil {
		s := *p.Push
		return &s
	}
	return nil
}

// ---- reading ----

// PipelineFile is a pipeline file as the editor opens it: the text, and the
// branch and commit it came from.
type PipelineFile struct {
	Ref         string `json:"ref"`  // the branch or tag
	Kind        string `json:"kind"` // branch or tag
	SHA         string `json:"sha"`  // the commit the text came from
	Subject     string `json:"subject"`
	Author      string `json:"author"`
	CommittedAt int64  `json:"committed_at"`
	Path        string `json:"path"`   // .launchway/pipeline.yaml, or .yml if that is what the repository has
	Exists      bool   `json:"exists"` // false: there is no file yet, and Text is a starting point
	// Configured is true when there is no file and the project runs the
	// pipeline configured on the server; Text is then that pipeline.
	Configured bool   `json:"configured"`
	Text       string `json:"text"`
}

// ReadPipelineFile reads the pipeline file at the tip of a branch or tag,
// fetching first unless the mirror was fetched in the last few seconds. An
// empty ref means the default branch. A commit without a pipeline file gives
// a starter pipeline with Exists false.
func (m *Manager) ReadPipelineFile(ctx context.Context, name, ref string) (*PipelineFile, error) {
	p := m.c().projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	f := &PipelineFile{Ref: r.Name, Kind: r.Kind, SHA: r.SHA, Subject: r.Subject, Author: r.Author, CommittedAt: r.CommittedAt}
	f.Path, f.Text, err = p.fileAt(ctx, r.SHA)
	switch {
	case errors.Is(err, ErrNoPipeline) && p.Pipeline != nil:
		f.Configured = true
		if f.Text, err = configuredText(name, p.Pipeline); err != nil {
			return nil, err
		}
	case errors.Is(err, ErrNoPipeline):
		f.Text = pipeline.Starter(name)
	case err != nil:
		return nil, err
	default:
		f.Exists = true
	}
	return f, nil
}

// resolve fetches (unless fresh) and finds a branch or tag by name. With
// branchOnly, tags are not accepted.
func (p *project) resolve(ctx context.Context, ref string, branchOnly bool) (Ref, error) {
	if err := p.fetch(ctx, 3*time.Second); err != nil {
		return Ref{}, err
	}
	refs, err := p.refs(ctx)
	if err != nil {
		return Ref{}, err
	}
	if ref == "" {
		ref = p.DefaultBranch
	}
	for _, r := range refs {
		if branchOnly && r.Kind != "branch" {
			continue
		}
		if r.Name == ref || r.FullRef() == ref {
			return r, nil
		}
	}
	if branchOnly {
		return Ref{}, fmt.Errorf("%w: no branch called %q", engine.ErrNotFound, ref)
	}
	return Ref{}, fmt.Errorf("%w: no branch or tag called %q", engine.ErrNotFound, ref)
}

// fileAt returns the pipeline file at a commit: its path and text, or
// ErrNoPipeline if there is none.
func (p *project) fileAt(ctx context.Context, sha string) (string, string, error) {
	for _, file := range PipelinePaths {
		data, err := gitOut(ctx, p.dir, "show", sha+":"+file)
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
				continue
			}
			return "", "", err
		}
		return file, data, nil
	}
	return PipelinePaths[0], "", ErrNoPipeline
}

// ---- draft runs ----

// DraftRequest asks for a draft run: pipeline text that is not in Git, run
// against the current tip of a branch or tag.
type DraftRequest struct {
	Project string
	Ref     string // a branch or tag; empty means the default branch
	Text    string
	Actor   string // who asked
	Secrets bool   // whether its steps may read secrets
	// Libraries overrides the versions of libraries the text uses, as for
	// StartRun.
	Libraries map[string]string
	// Inputs are values for the pipeline's inputs; those not given take
	// their defaults.
	Inputs map[string]string
}

// StartDraft checks the text with the same parser as every pipeline, finds
// the commit, and starts a draft run. Problems in the text are returned as
// pipeline.Problems, before anything is fetched.
func (m *Manager) StartDraft(ctx context.Context, d DraftRequest) (*engine.RunSummary, error) {
	p := m.c().projects[d.Project]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, d.Project)
	}
	if err := CheckOverrides(d.Libraries); err != nil {
		return nil, err
	}
	pl, err := pipeline.Parse([]byte(d.Text))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	r, err := p.resolve(ctx, d.Ref, false)
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	pl, libs, vals, err := m.prepare(ctx, pl, d.Inputs, d.Libraries)
	if err != nil {
		return nil, err
	}
	t := trigger(r, pipeline.EventDraft, d.Actor)
	t.Inputs = vals
	return m.eng.CreateRun(engine.NewRun{Project: d.Project, Pipeline: pl, Trigger: t,
		Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}, Draft: &engine.Draft{Text: d.Text, Secrets: d.Secrets}})
}

// ---- saving ----

// Author is the person a commit is made for.
type Author struct {
	User  string // the username, for the branch name and the fallback address
	Name  string // display name; the username if empty
	Email string // the address the provider gave; a no-reply address if empty
}

// SaveRequest asks to commit a pipeline file.
type SaveRequest struct {
	Project string
	BaseRef string // the branch the text was opened from
	BaseSHA string // the commit it was opened at, which must still be the branch's tip
	Text    string
	Message string // the commit message; DefaultCommitMessage if empty
	// Branch is where to commit: empty for a new branch named after the
	// author and the time, or BaseRef to commit to it directly, which the
	// project's push.allow_direct must allow.
	Branch string
	// Force saves text that does not validate. Whoever calls must have
	// decided that the person may (the API lets only admins).
	Force  bool
	Author Author
	Now    time.Time
}

// SaveResult is what a save made.
type SaveResult struct {
	Branch         string `json:"branch"`
	SHA            string `json:"sha"`
	BaseRef        string `json:"base_ref"`
	BaseSHA        string `json:"base_sha"`
	Path           string `json:"path"`
	Direct         bool   `json:"direct"`                     // committed to BaseRef itself
	PullRequestURL string `json:"pull_request_url,omitempty"` // where to open a pull request, for hosts that are recognised
}

// DefaultCommitMessage is used when a save gives none.
const DefaultCommitMessage = "Update pipeline via Launchway"

// Committer is who every save is committed by; the author is the person.
const (
	CommitterName  = "Launchway"
	CommitterEmail = "launchway@users.noreply.launchway"
	noReplyDomain  = "users.noreply.launchway"
)

// Limits on what a save may carry.
const (
	MaxCommitMessage = 4000
	newBranchTries   = 5
)

var shaRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// PushError is a push the repository refused or that could not reach it.
// Message is Git's own explanation, with credentials removed.
type PushError struct{ Message string }

func (e *PushError) Error() string { return "pushing to the repository failed: " + e.Message }

// ErrNotSavable wraps engine.ErrForbidden: the project has no push settings.
var ErrNotSavable = fmt.Errorf("%w: saving is not set up for this project (its configuration has no push: section)", engine.ErrForbidden)

// Save commits a pipeline file and pushes it. The commit is made in a
// temporary bare clone of this server's mirror, removed again whatever
// happens, and pushed with a lease, so that two people (or two servers)
// saving at once cannot overwrite each other: the second either gets a
// branch of its own or a conflict. Nothing is kept in memory.
func (m *Manager) Save(ctx context.Context, req SaveRequest) (*SaveResult, error) {
	p := m.c().projects[req.Project]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, req.Project)
	}
	if p.Push == nil {
		return nil, ErrNotSavable
	}
	if !shaRe.MatchString(req.BaseSHA) {
		return nil, fmt.Errorf("%w: base_sha must be the full commit id the text was opened at", engine.ErrInvalid)
	}
	if req.BaseRef == "" {
		return nil, fmt.Errorf("%w: base_ref must name the branch the text was opened from", engine.ErrInvalid)
	}
	direct := false
	switch req.Branch {
	case "":
	case req.BaseRef:
		if !p.Push.directAllowed(req.BaseRef) {
			return nil, fmt.Errorf("%w: %s may not be committed to directly in %s; save to a new branch and open a pull request",
				engine.ErrForbidden, req.BaseRef, req.Project)
		}
		direct = true
	default:
		return nil, fmt.Errorf("%w: branch must be empty (a new branch) or the base branch %s (a direct save)", engine.ErrInvalid, req.BaseRef)
	}
	if len(req.Text) > pipeline.MaxFileBytes {
		return nil, fmt.Errorf("%w: the pipeline is larger than %d KiB", engine.ErrInvalid, pipeline.MaxFileBytes>>10)
	}
	if _, err := pipeline.Parse([]byte(req.Text)); err != nil && !req.Force {
		return nil, err
	}
	msg, err := commitMessage(req.Message)
	if err != nil {
		return nil, err
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}

	tmpRoot := filepath.Join(m.dataDir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tmpRoot, savePrefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo.git")

	// Check the base against a fresh fetch and take a copy of it, holding
	// the mirror only for as long as that takes.
	p.mu.Lock()
	base, err := p.checkBase(ctx, req.BaseRef, req.BaseSHA)
	if err == nil {
		_, err = gitOut(ctx, "", "clone", "--quiet", "--bare", "--no-tags", "--single-branch", "--branch", base.Name, p.dir, repo)
	}
	var file string
	if err == nil {
		file, _, err = p.fileAt(ctx, base.SHA)
		if errors.Is(err, ErrNoPipeline) {
			err = nil
		}
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}

	sha, err := commitFile(ctx, repo, tmp, base.SHA, file, req.Text, msg, req.Author, req.Now)
	if err != nil {
		return nil, err
	}
	res := &SaveResult{BaseRef: base.Name, BaseSHA: base.SHA, Path: file, Direct: direct, SHA: sha}
	if direct {
		if err := push(ctx, repo, p.URL, sha, base.Name, base.SHA); err != nil {
			if errors.Is(err, errStale) {
				return nil, fmt.Errorf("%w: %s moved on while saving; nothing was saved. Load the branch again and reapply your change", engine.ErrConflict, base.Name)
			}
			return nil, err
		}
		res.Branch = base.Name
	} else {
		name, err := newBranchName(ctx, p.Push.BranchPrefix, req.Author.User, req.Now)
		if err != nil {
			return nil, err
		}
		for i := 1; ; i++ {
			try := name
			if i > 1 {
				try = fmt.Sprintf("%s-%d", name, i)
			}
			err = push(ctx, repo, p.URL, sha, try, "")
			if err == nil {
				res.Branch = try
				break
			}
			if !errors.Is(err, errStale) {
				return nil, err
			}
			if i == newBranchTries {
				return nil, fmt.Errorf("%w: branches named %s already exist; try again in a second", engine.ErrConflict, name)
			}
		}
		res.PullRequestURL = PullRequestURL(p.URL, base.Name, res.Branch)
	}
	// Show the new commit in branch lists at once. A failure here is not the
	// save's: the next poll fetches it.
	p.mu.Lock()
	if err := p.fetch(ctx, 0); err != nil {
		m.log.Warn("fetching after a save", "project", p.Name, "err", redact.URLs(err.Error()))
	}
	p.mu.Unlock()
	return res, nil
}

// checkBase fetches the mirror and confirms that the branch's tip is still
// the commit the text was opened at.
func (p *project) checkBase(ctx context.Context, branch, sha string) (Ref, error) {
	if err := p.fetch(ctx, 0); err != nil {
		return Ref{}, err
	}
	refs, err := p.refs(ctx)
	if err != nil {
		return Ref{}, err
	}
	for _, r := range refs {
		if r.Kind == "branch" && r.Name == branch {
			if r.SHA != sha {
				return Ref{}, fmt.Errorf("%w: %s has moved on since this text was opened: it was at %s and is now at %s (%q by %s). "+
					"Nothing was saved. Load the branch again and reapply your change",
					engine.ErrConflict, branch, short(sha), short(r.SHA), r.Subject, r.Author)
			}
			return r, nil
		}
	}
	return Ref{}, fmt.Errorf("%w: no branch called %q", engine.ErrNotFound, branch)
}

// commitMessage checks and tidies a commit message.
func commitMessage(msg string) (string, error) {
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(msg, ""))
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = DefaultCommitMessage
	}
	if len(msg) > MaxCommitMessage {
		return "", fmt.Errorf("%w: the commit message is longer than %d bytes", engine.ErrInvalid, MaxCommitMessage)
	}
	return msg + "\n", nil
}

// commitFile writes text to file on top of base in the bare repository and
// returns the new commit. It uses a temporary index in dir, so nothing else
// in the repository changes; no hooks run.
func commitFile(ctx context.Context, repo, dir, base, file, text, msg string, a Author, now time.Time) (string, error) {
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	if _, err := gitRun(ctx, repo, env, "", "read-tree", base); err != nil {
		return "", err
	}
	blob, err := gitRun(ctx, repo, env, text, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err := gitRun(ctx, repo, env, "", "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+","+file); err != nil {
		return "", err
	}
	tree, err := gitRun(ctx, repo, env, "", "write-tree")
	if err != nil {
		return "", err
	}
	date := fmt.Sprintf("%d +0000", now.Unix())
	name, email := authorLine(a)
	env = append(env,
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME="+CommitterName, "GIT_COMMITTER_EMAIL="+CommitterEmail, "GIT_COMMITTER_DATE="+date)
	sha, err := gitRun(ctx, repo, env, msg, "commit-tree", strings.TrimSpace(tree), "-p", base, "-F", "-")
	return strings.TrimSpace(sha), err
}

// authorLine returns the name and address a person's commits are authored
// with: what the provider gave, else their username and a no-reply address.
func authorLine(a Author) (name, email string) {
	clean := func(s string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '<' || r == '>' || unicode.IsControl(r) {
				return -1
			}
			return r
		}, s))
	}
	name = clean(a.Name)
	if name == "" {
		name = clean(a.User)
	}
	if name == "" {
		name = "Launchway user"
	}
	email = clean(a.Email)
	if email == "" || strings.ContainsAny(email, " \"\\,;") || strings.Count(email, "@") != 1 {
		email = slug(a.User, "user") + "@" + noReplyDomain
	}
	return name, email
}

// slug turns a username into something usable in a branch name or an
// address: lower-case letters, digits, '.', '_' and '-'.
func slug(s, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		ok := r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-')
		if !ok {
			if !dash {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		b.WriteRune(r)
		dash = r == '-'
	}
	out := strings.Trim(b.String(), "-._")
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".")
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-._")
	}
	if out == "" {
		return fallback
	}
	return out
}

// newBranchName returns prefix + the person + the time, checked by Git.
func newBranchName(ctx context.Context, prefix, user string, now time.Time) (string, error) {
	name := prefix + slug(user, "user") + "/" + now.UTC().Format("20060102-150405")
	if _, err := gitOut(ctx, "", "check-ref-format", "refs/heads/"+name); err != nil {
		return "", fmt.Errorf("%w: %q is not a valid branch name: %v", engine.ErrInvalid, name, err)
	}
	return name, nil
}

// errStale means the push lease failed: the branch was not where expected.
var errStale = errors.New("the branch is not where it was expected")

// push sends commit to branch at remote, only if the branch is at expect
// there (empty: only if it does not exist). It is a lease, checked by the
// receiving repository, so two pushes cannot both win.
func push(ctx context.Context, repo, remote, commit, branch, expect string) error {
	ref := "refs/heads/" + branch
	out, err := gitRun(ctx, repo, nil, "", "push", "--porcelain", "--no-verify",
		"--force-with-lease="+ref+":"+expect, remote, commit+":"+ref)
	if err == nil {
		return nil
	}
	// The lease failed, or another push created or moved the branch between
	// the lease check and the update.
	for _, sign := range []string{"(stale info)", "cannot lock ref", "reference already exists", "but expected"} {
		if strings.Contains(out, sign) || strings.Contains(err.Error(), sign) {
			return errStale
		}
	}
	return &PushError{Message: redact.URLs(err.Error())}
}

// PullRequestURL is where to open a pull (or merge) request from branch
// into base, for repositories on GitHub, GitLab, Gitea or Forgejo, judged by
// the host name. Otherwise, or for a URL it cannot read, it is "". It never
// contains credentials, and no Git host is contacted.
func PullRequestURL(remote, base, branch string) string {
	host, repoPath, scheme := parseRemote(remote)
	if host == "" || repoPath == "" {
		return ""
	}
	repoPath = strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git")
	if strings.Count(repoPath, "/") < 1 {
		return ""
	}
	hostname := strings.ToLower(host)
	if h, _, ok := strings.Cut(hostname, ":"); ok {
		hostname = h
	}
	root := scheme + "://" + host + "/" + escapePath(repoPath)
	switch {
	case hostname == "github.com" || strings.HasPrefix(hostname, "github."):
		return root + "/compare/" + escapePath(base) + "..." + escapePath(branch) + "?expand=1"
	case hostname == "gitlab.com" || strings.Contains(hostname, "gitlab"):
		q := url.Values{}
		q.Set("merge_request[source_branch]", branch)
		q.Set("merge_request[target_branch]", base)
		return root + "/-/merge_requests/new?" + q.Encode()
	case strings.Contains(hostname, "gitea") || strings.Contains(hostname, "forgejo") || hostname == "codeberg.org":
		return root + "/compare/" + escapePath(base) + "..." + escapePath(branch)
	}
	return ""
}

// parseRemote reads the host and path of a Git URL: https://, http://,
// ssh://, or scp-like user@host:path. Credentials and SSH ports are dropped.
func parseRemote(remote string) (host, repoPath, scheme string) {
	if !strings.Contains(remote, "://") {
		// user@host:path, the scp-like form; a local path has no colon before a slash.
		at := strings.LastIndex(remote, "@")
		colon := strings.Index(remote, ":")
		if colon <= 0 || (strings.Contains(remote[:colon], "/")) {
			return "", "", ""
		}
		host = remote[:colon]
		if at >= 0 && at < colon {
			host = remote[at+1 : colon]
		}
		return host, remote[colon+1:], "https"
	}
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return "", "", ""
	}
	switch u.Scheme {
	case "https", "http":
		return u.Host, u.Path, u.Scheme
	case "ssh", "git", "git+ssh", "ssh+git":
		return u.Hostname(), u.Path, "https"
	}
	return "", "", ""
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// savePrefix names the temporary directories saves work in.
const savePrefix = "save-"

// removeStaleSaves deletes temporary directories that a save left behind
// because the server stopped in the middle of it.
func removeStaleSaves(dataDir string, olderThan time.Duration) {
	root := filepath.Join(dataDir, "tmp")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), savePrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > olderThan {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
