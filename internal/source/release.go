package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/release"
)

// What releases need of Git, read from the project's mirror: which commit a
// branch or tag is at, the commits between two releases, how a tag stands,
// and the writes a release makes (a tag, and a branch with the changelog for a
// pull request). A project can be written to when its configuration has a push
// section (see PushSettings); without it a release adopts a tag that already
// exists.

// CommitInfo is a commit as release notes read it.
type CommitInfo struct {
	SHA     string
	Subject string
	Body    string
	Author  string
	Time    int64 // milliseconds
	Parents int
}

// TagInfo says how a tag stands.
type TagInfo struct {
	Exists    bool
	Name      string
	Object    string // the tag object's id (for an annotated tag) or the commit's
	Commit    string // the commit it points to
	Annotated bool
}

var tagNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,99}$`)

// CheckTagName refuses names that Git or people would trip over.
func CheckTagName(name string) error {
	if !tagNameRe.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") || strings.Contains(name, "/.") {
		return fmt.Errorf("%w: %q is not a usable tag name", engine.ErrInvalid, name)
	}
	return nil
}

func (m *Manager) releaseProject(name string) (*project, error) {
	p := m.c().projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	return p, nil
}

// ResolveRef finds the commit of a branch or tag after a fresh fetch.
func (m *Manager) ResolveRef(ctx context.Context, name, ref string) (Ref, error) {
	p, err := m.releaseProject(name)
	if err != nil {
		return Ref{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, ref, false)
	if err != nil {
		return Ref{}, err
	}
	// A tag's Ref may carry the tag object; the commit is what is released.
	if out, err := gitOut(ctx, p.dir, "rev-parse", "--verify", "--quiet", r.SHA+"^{commit}"); err == nil {
		r.SHA = strings.TrimSpace(out)
	}
	return r, nil
}

// ReleaseTag reads a tag.
func (m *Manager) ReleaseTag(ctx context.Context, name, tag string) (TagInfo, error) {
	p, err := m.releaseProject(name)
	if err != nil {
		return TagInfo{}, err
	}
	if err := CheckTagName(tag); err != nil {
		return TagInfo{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fetch(ctx, 3*time.Second); err != nil {
		return TagInfo{}, err
	}
	return tagInfo(ctx, p.dir, tag)
}

func tagInfo(ctx context.Context, dir, tag string) (TagInfo, error) {
	ti := TagInfo{Name: tag}
	obj, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag)
	if err != nil || strings.TrimSpace(obj) == "" {
		return ti, nil
	}
	ti.Exists, ti.Object = true, strings.TrimSpace(obj)
	c, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return ti, fmt.Errorf("%w: the tag %s does not point to a commit", engine.ErrConflict, tag)
	}
	ti.Commit = strings.TrimSpace(c)
	if t, err := gitOut(ctx, dir, "cat-file", "-t", ti.Object); err == nil {
		ti.Annotated = strings.TrimSpace(t) == "tag"
	}
	return ti, nil
}

// CommitsBetween lists the commits reachable from to and not from from, newest
// first (from empty: the whole history, up to a limit).
func (m *Manager) CommitsBetween(ctx context.Context, name, from, to string) ([]CommitInfo, error) {
	p, err := m.releaseProject(name)
	if err != nil {
		return nil, err
	}
	for _, s := range []string{from, to} {
		if s != "" && !shaRe.MatchString(s) {
			return nil, fmt.Errorf("%w: %q is not a commit id", engine.ErrInvalid, s)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	out, err := gitOut(ctx, p.dir, "log", "--max-count=2000", "--format=%H%x1f%P%x1f%an%x1f%ct%x1f%s%x1f%b%x1e", rng)
	if err != nil {
		return nil, err
	}
	var list []CommitInfo
	var joined [][2]string
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 6)
		if len(f) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(f[3], 10, 64)
		list = append(list, CommitInfo{SHA: f[0], Parents: len(strings.Fields(f[1])), Author: f[2], Time: ts * 1000, Subject: f[4], Body: strings.TrimSpace(f[5])})
		if ps := strings.Fields(f[1]); len(ps) == 2 && mergePR.MatchString(f[4]) {
			joined = append(joined, [2]string{ps[0], ps[1]})
		}
	}
	// The commits a merged pull request brought are part of that pull request: listing them again beside it would
	// say each change twice in the notes.
	if len(joined) > 0 {
		inPull := map[string]bool{}
		for _, j := range joined {
			if out, err := gitOut(ctx, p.dir, "rev-list", "--max-count=500", j[0]+".."+j[1]); err == nil {
				for _, c := range strings.Fields(out) {
					inPull[c] = true
				}
			}
		}
		kept := list[:0]
		for _, c := range list {
			if !inPull[c.SHA] {
				kept = append(kept, c)
			}
		}
		list = kept
	}
	return list, nil
}

var (
	squashPR = regexp.MustCompile(`^(.*?)\s*\(#(\d+)\)$`)
	mergePR  = regexp.MustCompile(`^Merge pull request #(\d+)\b`)
)

// ChangesFromCommits turns commits into changes: a merge of a pull request or
// a squash with "(#123)" in its subject is a pull; any other commit is a
// commit. Merge commits that only join branches are left out, since the
// commits they join are listed. url builds a link for a pull request number
// ("" for none).
func ChangesFromCommits(commits []CommitInfo, pullURL func(n int) string) []release.Change {
	var out []release.Change
	for _, c := range commits {
		switch {
		case mergePR.MatchString(c.Subject):
			n, _ := strconv.Atoi(mergePR.FindStringSubmatch(c.Subject)[1])
			title := firstLine(c.Body)
			if title == "" {
				title = c.Subject
			}
			ch := release.Change{Kind: "pull", Ref: n, SHA: c.SHA, Title: title, Body: c.Body, Author: c.Author}
			if pullURL != nil {
				ch.URL = pullURL(n)
			}
			out = append(out, ch)
		case c.Parents > 1 && strings.HasPrefix(c.Subject, "Merge "):
			continue
		case squashPR.MatchString(c.Subject):
			m := squashPR.FindStringSubmatch(c.Subject)
			n, _ := strconv.Atoi(m[2])
			ch := release.Change{Kind: "pull", Ref: n, SHA: c.SHA, Title: m[1], Body: c.Body, Author: c.Author}
			if pullURL != nil {
				ch.URL = pullURL(n)
			}
			out = append(out, ch)
		default:
			out = append(out, release.Change{Kind: "commit", SHA: c.SHA, Title: c.Subject, Body: c.Body, Author: c.Author})
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// DiffSummary names what changed between two commits.
type DiffSummary struct {
	Files    []string `json:"files"`
	More     int      `json:"more,omitempty"`
	Stat     string   `json:"stat"`
	Manifest []string `json:"manifests"` // files that look like deployment configuration
}

var manifestRe = regexp.MustCompile(`(?i)(^|/)(deploy|k8s|kubernetes|helm|charts?|manifests?|kustomize|overlays|environments?)/|(^|/)(Chart|values|kustomization)[^/]*\.ya?ml$|(^|/)(Dockerfile|docker-compose[^/]*\.ya?ml)$`)

// Compare summarizes the difference between two commits.
func (m *Manager) Compare(ctx context.Context, name, a, b string) (DiffSummary, error) {
	var d DiffSummary
	p, err := m.releaseProject(name)
	if err != nil {
		return d, err
	}
	if !shaRe.MatchString(a) || !shaRe.MatchString(b) {
		return d, fmt.Errorf("%w: compare two commit ids", engine.ErrInvalid)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out, err := gitOut(ctx, p.dir, "diff", "--name-only", a, b)
	if err != nil {
		return d, err
	}
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f == "" {
			continue
		}
		if manifestRe.MatchString(f) {
			d.Manifest = append(d.Manifest, f)
		}
		if len(d.Files) < 200 {
			d.Files = append(d.Files, f)
		} else {
			d.More++
		}
	}
	if s, err := gitOut(ctx, p.dir, "diff", "--shortstat", a, b); err == nil {
		d.Stat = strings.TrimSpace(s)
	}
	return d, nil
}

// WritableForReleases says whether Launchway may write a tag and a changelog
// branch to the project's repository.
func (m *Manager) WritableForReleases(name string) bool {
	p := m.c().projects[name]
	return p != nil && p.Push != nil
}

// CreateTag makes an annotated tag at a commit and pushes it. It refuses if
// the tag exists anywhere already (a tag is never moved), and if the project
// has no push settings.
func (m *Manager) CreateTag(ctx context.Context, name, tag, sha, message string, by Author, now time.Time) (TagInfo, error) {
	p, err := m.releaseProject(name)
	if err != nil {
		return TagInfo{}, err
	}
	if p.Push == nil {
		return TagInfo{}, ErrNotSavable
	}
	if err := CheckTagName(tag); err != nil {
		return TagInfo{}, err
	}
	if !shaRe.MatchString(sha) {
		return TagInfo{}, fmt.Errorf("%w: %q is not a commit id", engine.ErrInvalid, sha)
	}
	tmpRoot := filepath.Join(m.dataDir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return TagInfo{}, err
	}
	tmp, err := os.MkdirTemp(tmpRoot, "tag-")
	if err != nil {
		return TagInfo{}, err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo.git")
	p.mu.Lock()
	err = p.fetch(ctx, 0)
	if err == nil {
		var cur TagInfo
		if cur, err = tagInfo(ctx, p.dir, tag); err == nil && cur.Exists {
			p.mu.Unlock()
			return cur, fmt.Errorf("%w: the tag %s already exists (at %s); a tag is never moved: adopt it, or release under another version", engine.ErrConflict, tag, shortSHA(cur.Commit))
		}
	}
	if err == nil {
		_, err = gitOut(ctx, "", "clone", "--quiet", "--bare", "--no-tags", p.dir, repo)
	}
	p.mu.Unlock()
	if err != nil {
		return TagInfo{}, err
	}
	n, e := authorLine(by)
	date := fmt.Sprintf("%d +0000", now.Unix())
	env := []string{"GIT_COMMITTER_NAME=" + n, "GIT_COMMITTER_EMAIL=" + e, "GIT_COMMITTER_DATE=" + date}
	if strings.TrimSpace(message) == "" {
		message = "Release " + tag
	}
	if _, err := gitRun(ctx, repo, env, message+"\n", "tag", "-a", "-F", "-", tag, sha); err != nil {
		return TagInfo{}, err
	}
	remote, err := remoteEnv(ctx, p.URL)
	if err != nil {
		return TagInfo{}, &PushError{Message: redact.URLs(err.Error())}
	}
	ref := "refs/tags/" + tag
	if out, err := gitRun(ctx, repo, remote, "", "push", "--porcelain", "--no-verify", "--force-with-lease="+ref+":", p.URL, ref+":"+ref); err != nil {
		for _, sign := range []string{"(stale info)", "already exists", "but expected", "rejected"} {
			if strings.Contains(out, sign) || strings.Contains(err.Error(), sign) {
				return TagInfo{}, fmt.Errorf("%w: the tag %s was made by someone else in the meantime", engine.ErrConflict, tag)
			}
		}
		return TagInfo{}, &PushError{Message: redact.URLs(err.Error())}
	}
	p.mu.Lock()
	_ = p.fetch(ctx, 0)
	ti, err := tagInfo(ctx, p.dir, tag)
	p.mu.Unlock()
	if err == nil && !ti.Exists {
		err = errors.New("the tag was pushed but is not visible yet; try adopting it in a moment")
	}
	return ti, err
}

func shortSHA(s string) string { return s[:min(8, len(s))] }

// ChangelogProposal is a branch with the changelog updated, for a pull request.
type ChangelogProposal struct {
	Branch         string `json:"branch"`
	Base           string `json:"base"`
	Path           string `json:"path"`
	SHA            string `json:"sha"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
}

// ProposeChangelog commits the notes of a release at the top of the
// changelog file in a new branch and pushes it, never to the base branch
// itself. The branch is then offered as a pull request.
func (m *Manager) ProposeChangelog(ctx context.Context, name, base, file, version, notes string, by Author, now time.Time) (*ChangelogProposal, error) {
	p, err := m.releaseProject(name)
	if err != nil {
		return nil, err
	}
	if p.Push == nil {
		return nil, ErrNotSavable
	}
	tmpRoot := filepath.Join(m.dataDir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tmpRoot, "changelog-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo.git")
	p.mu.Lock()
	var b Ref
	if err = p.fetch(ctx, 0); err == nil {
		b, err = p.resolve(ctx, base, true)
	}
	var existing string
	if err == nil {
		if existing, err = gitOut(ctx, p.dir, "show", b.SHA+":"+file); err != nil {
			if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
				existing, err = "", nil
			}
		}
	}
	if err == nil {
		_, err = gitOut(ctx, "", "clone", "--quiet", "--bare", "--no-tags", "--single-branch", "--branch", b.Name, p.dir, repo)
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	text, err := release.ChangelogEntry(existing, version, notes)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", engine.ErrConflict, err)
	}
	sha, err := commitFile(ctx, repo, tmp, b.SHA, file, text, "Changelog for "+version+"\n", by, now)
	if err != nil {
		return nil, err
	}
	prefix := p.Push.BranchPrefix
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}
	branch := prefix + "changelog-" + slug(version, "release")
	if err := push(ctx, repo, p.URL, sha, branch, ""); err != nil {
		if errors.Is(err, errStale) {
			return nil, fmt.Errorf("%w: the branch %s already exists; a changelog for %s was proposed before", engine.ErrConflict, branch, version)
		}
		return nil, err
	}
	return &ChangelogProposal{Branch: branch, Base: b.Name, Path: file, SHA: sha, PullRequestURL: PullRequestURL(p.URL, b.Name, branch)}, nil
}

// ProjectRepoURL is the project's repository address with no credentials, for links.
func (m *Manager) ProjectRepoURL(name string) string {
	if p := m.c().projects[name]; p != nil {
		return redact.URLs(p.URL)
	}
	return ""
}
