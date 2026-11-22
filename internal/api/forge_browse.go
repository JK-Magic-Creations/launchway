package api

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
)

// Reading a repository's code in the browser, the CLI and the tools: branches
// and tags, one commit, what a change touches, blame, search and a file's raw
// bytes. Every route here asks for the same permission as a clone (repoRead),
// except making and deleting a branch, which ask for what a push asks for.

func (s *Server) browseRoutes() []route {
	const base = "/api/v1/repositories/{owner}/{name}"
	return []route{
		{"GET " + base + "/branches", repoRead, "", s.listBranches},
		{"POST " + base + "/branches", repoWrite, "repository.branch_create", s.createBranch},
		{"DELETE " + base + "/branches", repoWrite, "repository.branch_delete", s.deleteBranch},
		{"POST " + base + "/branches/rename", repoWrite, "repository.branch_rename", s.renameBranch},
		{"GET " + base + "/tags", repoRead, "", s.listTags},
		{"POST " + base + "/tags", repoWrite, "repository.tag_create", s.createTag},
		{"DELETE " + base + "/tags", repoWrite, "repository.tag_delete", s.deleteTag},
		{"GET " + base + "/commits/{sha}", repoRead, "", s.getCommit},
		{"GET " + base + "/diff", repoRead, "", s.repoDiff},
		{"GET " + base + "/diff/patch", repoRead, "", s.repoPatch},
		{"GET " + base + "/blame", repoRead, "", s.blame},
		{"GET " + base + "/search", repoRead, "", s.searchCode},
		{"GET " + base + "/raw", repoRead, "", s.rawFile},
	}
}

// pinned says a ref is a full commit name: what it names never changes, so
// the answer may be kept for good.
func pinned(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// cacheFor sets how long a browser may keep an answer: for ever when it is
// pinned to a commit, but only for this person (the repository may be private).
func cacheFor(w http.ResponseWriter, ref string) {
	if pinned(ref) {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
}

// A small token bucket per signed-in person for the two expensive reads.
type bucket struct {
	mu   sync.Mutex
	last map[string]time.Time
	n    map[string]float64
}

var searchBuckets, blameBuckets = &bucket{last: map[string]time.Time{}, n: map[string]float64{}}, &bucket{last: map[string]time.Time{}, n: map[string]float64{}}

// take spends one token of a bucket that holds burst and refills perMinute.
func (b *bucket) take(who string, burst, perMinute float64, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.last) > 5000 {
		b.last, b.n = map[string]time.Time{}, map[string]float64{}
	}
	tokens, ok := b.n[who]
	if !ok {
		tokens = burst
	} else {
		tokens = min(burst, tokens+now.Sub(b.last[who]).Minutes()*perMinute)
	}
	b.last[who] = now
	if tokens < 1 {
		b.n[who] = tokens
		return false
	}
	b.n[who] = tokens - 1
	return true
}

func (s *Server) limitRead(w http.ResponseWriter, r *http.Request, b *bucket, burst, perMinute float64, what string) bool {
	id := identityFrom(r)
	who := id.User
	if who == "" {
		who = s.clientAddr(r)
	}
	if b.take(who, burst, perMinute, s.Now()) {
		return true
	}
	w.Header().Set("Retry-After", "10")
	writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("%s is limited to %d a minute; wait a few seconds", what, int(perMinute)))
	return false
}

func intq(r *http.Request, k string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(k))
	return n
}

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	base := r.URL.Query().Get("base")
	if base == "" {
		base = repo.DefaultBranch
	}
	items, total, err := s.filesService().Refs(r.Context(), repo.ID, "branch", r.URL.Query().Get("q"), base, intq(r, "limit"))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	pats, _ := s.protectedPatterns(r, repo)
	type branch struct {
		files.RefInfo
		Default    bool   `json:"default"`
		Protected  bool   `json:"protected"`
		OpenPull   int    `json:"open_pull,omitempty"`
		BaseBranch string `json:"base"`
	}
	out := make([]branch, len(items))
	for i, it := range items {
		b := branch{RefInfo: it, Default: it.Name == repo.DefaultBranch, BaseBranch: base}
		_, b.Protected = refrules.Match(pats, "refs/heads/"+it.Name)
		if s.Forge.Pulls != nil && i < 100 {
			if open, err := s.Forge.Pulls.Store.OpenFromSource(r.Context(), repo.ID, it.Name); err == nil && len(open) > 0 {
				b.OpenPull = open[0].Number
			}
		}
		out[i] = b
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "default_branch": repo.DefaultBranch, "truncated": total > len(out)})
}

func (s *Server) protectedPatterns(r *http.Request, repo forge.Repository) ([]string, error) {
	if s.Forge.Rules == nil {
		return nil, nil
	}
	return s.Forge.Rules.Patterns(r.Context(), repo.ID)
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	items, total, err := s.filesService().Refs(r.Context(), repo.ID, "tag", r.URL.Query().Get("q"), "", intq(r, "limit"))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "truncated": total > len(items)})
}

type branchIn struct {
	Name string `json:"name"`
	From string `json:"from"`
}

// writePolicy is the same check a push gets, for a change the server makes.
func (s *Server) writePolicy(ctx context.Context, r *http.Request, repo forge.Repository) func(ref, old, next string) error {
	id := identityFrom(r)
	policy := s.pushPolicy(gitCaller{id: id, user: id.User}, repo)
	return func(ref, old, next string) error {
		if policy == nil {
			return nil
		}
		if err := policy(ctx, []githttp.RefUpdate{{Name: ref, Old: old, New: next}}); err != nil {
			return refusedByRules{err}
		}
		return nil
	}
}

func (s *Server) writableRepo(w http.ResponseWriter, r *http.Request) (forge.Repository, bool) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return repo, false
	}
	if s.Forge.Rules == nil {
		writeErr(w, http.StatusServiceUnavailable, "branch rules are not available; nothing was changed")
		return repo, false
	}
	if repo.Mirror || repo.Archived {
		writeErr(w, http.StatusConflict, repo.Path+" takes no changes: it is "+map[bool]string{true: "a mirror", false: "archived"}[repo.Mirror])
		return repo, false
	}
	return repo, true
}

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in branchIn
	if !decode(w, r, &in) {
		return
	}
	if in.From == "" {
		in.From = defaultRef(r, repo)
	}
	setAudit(r, repo.Path, in.Name+" from "+in.From)
	sha, err := s.filesService().CreateBranch(r.Context(), repo.ID, in.Name, in.From, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", in.Name+" at "+short(sha))
	s.afterServerCommit(r.Context(), gitCaller{id: identityFrom(r), user: identityFrom(r).User}, repo, "refs/heads/"+in.Name, strings.Repeat("0", len(sha)), sha)
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "commit": sha})
}

func (s *Server) deleteBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == repo.DefaultBranch {
		writeErr(w, http.StatusConflict, name+" is the default branch: make another branch the default first")
		return
	}
	setAudit(r, repo.Path, name)
	was, err := s.filesService().DeleteBranch(r.Context(), repo.ID, name, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", name+" was "+was)
	zero := strings.Repeat("0", len(was))
	s.afterServerCommit(r.Context(), gitCaller{id: identityFrom(r), user: identityFrom(r).User}, repo, "refs/heads/"+name, was, zero)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "was": was,
		"undo": "git push origin " + was + ":refs/heads/" + name + "  (or POST /api/v1/repositories/" + repo.Path + "/branches with {\"name\": \"" + name + "\", \"from\": \"" + was + "\"})"})
}

// renameIn is a branch rename.
type renameIn struct {
	Name    string `json:"name"`
	NewName string `json:"new_name"`
}

func (s *Server) renameBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in renameIn
	if !decode(w, r, &in) {
		return
	}
	if in.Name == repo.DefaultBranch {
		writeErr(w, http.StatusConflict, in.Name+" is the default branch: make another branch the default first, then rename this one")
		return
	}
	setAudit(r, repo.Path, in.Name+" to "+in.NewName)
	sha, err := s.filesService().RenameBranch(r.Context(), repo.ID, in.Name, in.NewName, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", in.Name+" to "+in.NewName+" at "+short(sha))
	caller := gitCaller{id: identityFrom(r), user: identityFrom(r).User}
	zero := strings.Repeat("0", len(sha))
	s.afterServerCommit(r.Context(), caller, repo, "refs/heads/"+in.NewName, zero, sha)
	s.afterServerCommit(r.Context(), caller, repo, "refs/heads/"+in.Name, sha, zero)
	writeJSON(w, http.StatusOK, map[string]any{"name": in.NewName, "was": in.Name, "commit": sha,
		"note": "Pull requests and runs that name " + in.Name + " are not moved; people with a clone rename theirs with git branch -m."})
}

func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in branchIn
	if !decode(w, r, &in) {
		return
	}
	if in.From == "" {
		in.From = defaultRef(r, repo)
	}
	setAudit(r, repo.Path, in.Name+" from "+in.From)
	sha, err := s.filesService().CreateTag(r.Context(), repo.ID, in.Name, in.From, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", in.Name+" at "+short(sha))
	s.afterServerCommit(r.Context(), gitCaller{id: identityFrom(r), user: identityFrom(r).User}, repo, "refs/tags/"+in.Name, strings.Repeat("0", len(sha)), sha)
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "commit": sha})
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	setAudit(r, repo.Path, name)
	was, err := s.filesService().DeleteTag(r.Context(), repo.ID, name, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", name+" was "+was)
	s.afterServerCommit(r.Context(), gitCaller{id: identityFrom(r), user: identityFrom(r).User}, repo, "refs/tags/"+name, was, strings.Repeat("0", len(was)))
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "was": was,
		"undo": "git push origin " + was + ":refs/tags/" + name + "  (or POST /api/v1/repositories/" + repo.Path + "/tags with {\"name\": \"" + name + "\", \"from\": \"" + was + "\"})"})
}

// getCommit is one commit with its parents, and the runs that were started for it.
func (s *Server) getCommit(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	c, err := s.filesService().CommitDetail(r.Context(), repo.ID, r.PathValue("sha"))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	runs := []engine.RunSummary{}
	if s.Engine != nil {
		if only, err := s.visibleProjects(identityFrom(r)); err == nil {
			if all, err := s.Engine.ListRuns(engine.RunFilter{Limit: 500, OnlyProjects: only}); err == nil {
				for _, run := range all {
					if run.SHA == c.SHA {
						runs = append(runs, *run)
					}
				}
			}
		}
	}
	w.Header().Set("Cache-Control", "private, no-cache") // the runs of a commit change
	writeJSON(w, http.StatusOK, map[string]any{"commit": c, "runs": runs})
}

// diffEnds resolves ?from= (a commit, or "root" for the empty tree) and ?to=.
func (s *Server) diffEnds(w http.ResponseWriter, r *http.Request) (repo forge.Repository, from, to string, ok bool) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok = s.repoOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var err error
	if to, err = s.Forge.Pulls.Resolve(r.Context(), repo.ID, q.Get("to")); err != nil {
		s.failPull(w, err)
		return repo, "", "", false
	}
	if q.Get("from") == "root" {
		from = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // the empty tree
	} else if from, err = s.Forge.Pulls.Resolve(r.Context(), repo.ID, q.Get("from")); err != nil {
		s.failPull(w, err)
		return repo, "", "", false
	}
	return repo, from, to, true
}

func (s *Server) repoDiff(w http.ResponseWriter, r *http.Request) {
	repo, from, to, ok := s.diffEnds(w, r)
	if !ok {
		return
	}
	c, err := s.Forge.Pulls.Compare(r.Context(), repo.ID, from, to, pulls.DefaultLimits)
	if err != nil {
		s.failPull(w, err)
		return
	}
	off, lim := max(intq(r, "offset"), 0), intq(r, "limit")
	if lim <= 0 || lim > 500 {
		lim = 100
	}
	fs, next := c.Files, 0
	if off >= len(fs) {
		fs = []pulls.FileChange{}
	} else {
		end := min(off+lim, len(fs))
		if end < len(fs) {
			next = end
		}
		fs = fs[off:end]
	}
	cacheFor(w, to)
	writeJSON(w, http.StatusOK, map[string]any{"from": c.From, "to": c.To, "files": fs, "total": c.Total, "additions": c.Additions, "deletions": c.Deletions, "truncated": c.Truncated, "next_offset": next})
}

func (s *Server) repoPatch(w http.ResponseWriter, r *http.Request) {
	repo, from, to, ok := s.diffEnds(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	full := q.Get("full") == "1" || q.Get("full") == "true"
	pt, err := s.Forge.Pulls.PatchOf(r.Context(), repo.ID, from, to, q["path"], full, pulls.DefaultLimits)
	if err != nil {
		s.failPull(w, err)
		return
	}
	cacheFor(w, to)
	writeJSON(w, http.StatusOK, pt)
}

func (s *Server) blame(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	if !s.limitRead(w, r, blameBuckets, 60, 120, "blame") {
		return
	}
	ref := defaultRef(r, repo)
	b, err := s.filesService().BlameOf(r.Context(), repo.ID, ref, r.URL.Query().Get("path"), intq(r, "from"), intq(r, "to"))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	cacheFor(w, ref)
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) searchCode(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	if !s.limitRead(w, r, searchBuckets, 20, 30, "code search") {
		return
	}
	q := r.URL.Query()
	var exts []string
	for _, e := range q["ext"] {
		exts = append(exts, strings.Split(e, ",")...)
	}
	in := q.Get("in")
	if in != "" && in != "content" && in != "path" && in != "both" {
		writeErr(w, http.StatusBadRequest, "in is content, path or both")
		return
	}
	res, err := s.filesService().Search(r.Context(), repo.ID, defaultRef(r, repo), files.SearchQuery{
		Pattern: q.Get("q"), Regex: q.Get("regex") == "1" || q.Get("regex") == "true", Case: q.Get("case") == "1" || q.Get("case") == "true",
		In: in, Dir: q.Get("dir"), Exts: exts, Limit: intq(r, "limit")})
	if err != nil {
		s.failFiles(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, http.StatusOK, res)
}

// inlineTypes are the only file types shown in a page rather than downloaded:
// pictures, which cannot run anything in an <img>. Even these are served with
// a sandbox policy so that opening one directly runs nothing either.
var inlineTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".svg": "image/svg+xml"}

// rawFile serves a file's bytes. The content is the repository's, written by
// anyone who could push, so it is never allowed to run in this origin: it is
// a forced download by default, never sniffed, and under a policy that
// forbids scripts, frames and everything else.
func (s *Server) rawFile(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	ref, p := defaultRef(r, repo), r.URL.Query().Get("path")
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
	cacheFor(w, ref)
	name := path.Base(p)
	ct, disp := "application/octet-stream", "attachment"
	if t, ok := inlineTypes[strings.ToLower(path.Ext(name))]; ok && r.URL.Query().Get("inline") == "1" {
		ct, disp = t, "inline"
	}
	sent := false
	err := s.filesService().Raw(r.Context(), repo.ID, ref, p, func(size int64, mode string) io.Writer {
		sent = true
		h.Set("Content-Type", ct)
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": asciiName(name)}))
		return w
	})
	if err != nil && !sent {
		h.Del("Content-Length")
		h.Set("Content-Type", "application/json")
		s.failFiles(w, err)
	}
}

// asciiName is a file name safe to put in a header.
func asciiName(n string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, n)
}
