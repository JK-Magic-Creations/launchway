package api

import (
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// What a pull request changes: files, patches (bounded, paged), commits, the
// changes since a revision, and the compare page that exists before a pull
// request does.

// diffSpan is the two commits a request asks to compare: the pull request's
// own (merge base to head), or from a revision's head to the current head
// (?since=N), or two commits of the repository (?from=&to=).
func (s *Server) diffSpan(w http.ResponseWriter, r *http.Request, repoID string, p pulls.PullRequest) (from, to string, ok bool) {
	q := r.URL.Query()
	switch {
	case q.Get("since") != "":
		n, err := strconv.Atoi(q.Get("since"))
		revs, rerr := s.Forge.Pulls.Store.Revisions(r.Context(), p.ID)
		if err != nil || rerr != nil {
			writeErr(w, http.StatusBadRequest, "since is a revision number of this pull request, such as 1")
			return "", "", false
		}
		for _, rv := range revs {
			if rv.Seq == n {
				return rv.HeadSHA, p.HeadSHA, true
			}
		}
		writeErr(w, http.StatusBadRequest, "this pull request has no revision "+q.Get("since"))
		return "", "", false
	case q.Get("from") != "" || q.Get("to") != "":
		var err error
		if from, err = s.Forge.Pulls.Resolve(r.Context(), repoID, q.Get("from")); err != nil {
			s.failPull(w, err)
			return "", "", false
		}
		if to, err = s.Forge.Pulls.Resolve(r.Context(), repoID, q.Get("to")); err != nil {
			s.failPull(w, err)
			return "", "", false
		}
		return from, to, true
	}
	from = p.MergeBase
	if from == "" {
		from = p.BaseSHA
	}
	return from, p.HeadSHA, true
}

func (s *Server) pullFiles(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	from, to, ok := s.diffSpan(w, r, repo.ID, p)
	if !ok {
		return
	}
	c, err := s.Forge.Pulls.Compare(r.Context(), repo.ID, from, to, pulls.DefaultLimits)
	if err != nil {
		s.failPull(w, err)
		return
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if lim <= 0 || lim > 500 {
		lim = 100
	}
	off = max(off, 0)
	files := c.Files
	next := 0
	if off >= len(files) {
		files = nil
	} else {
		end := min(off+lim, len(files))
		if end < len(files) {
			next = end
		}
		files = files[off:end]
	}
	if files == nil {
		files = []pulls.FileChange{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": c.From, "to": c.To, "files": files, "total": c.Total, "additions": c.Additions, "deletions": c.Deletions,
		"truncated": c.Truncated, "next_offset": next, "since": r.URL.Query().Get("since")})
}

// pullPatch serves the unified patch of one file (?path=, repeatable for a
// rename's two names) or of everything; a file over the limits is held back
// until the caller asks for it with ?full=1 ("load anyway").
func (s *Server) pullPatch(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	from, to, ok := s.diffSpan(w, r, repo.ID, p)
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
	writeJSON(w, http.StatusOK, pt)
}

func (s *Server) pullCommits(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	cs, err := s.Forge.Pulls.Commits(r.Context(), repo.ID, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cs})
}

func (s *Server) compareBranches(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	base, head := r.URL.Query().Get("base"), r.URL.Query().Get("head")
	if base == "" {
		base = repo.DefaultBranch
	}
	bc, err := s.compareHead(r, repo, base, head)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bc)
}
