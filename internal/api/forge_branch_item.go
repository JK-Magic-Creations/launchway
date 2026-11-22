package api

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// Starting a branch from a work item: the name comes from the item's key and title (safely slugged), the branch is
// made the way the branches route makes one (same rights, same rules), and the item and the branch are tied both
// ways: the item gets a branch link, and the branch list names the item.

// branchNameFor is the suggested branch name of an item: its key, then the title as lower-case words.
func branchNameFor(key, title string) string {
	var b strings.Builder
	dash := true
	put := func(s string) {
		for _, r := range strings.ToLower(s) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
				dash = false
			} else if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	put(key)
	if !dash {
		b.WriteByte('-')
		dash = true
	}
	words := b.Len()
	put(title)
	name := strings.Trim(b.String(), "-")
	const most = 60
	if len(name) > most {
		name = name[:most]
		if i := strings.LastIndexByte(name, '-'); i > words {
			name = name[:i]
		}
	}
	return strings.Trim(name, "-")
}

func (s *Server) suggestBranchName(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	it, err := s.work().Get(r.Context(), s.workCaller(r), r.URL.Query().Get("item"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"item": it.Ref, "name": branchNameFor(it.Ref, it.Title), "from": repo.DefaultBranch})
}

type branchFromItemIn struct {
	Item string `json:"item"`
	Name string `json:"name"`
	From string `json:"from"`
}

func (s *Server) branchFromItem(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in branchFromItemIn
	if !decode(w, r, &in) {
		return
	}
	c := s.workCaller(r)
	it, err := s.work().Get(r.Context(), c, in.Item)
	if err != nil {
		s.failWork(w, err)
		return
	}
	if in.Name == "" {
		in.Name = branchNameFor(it.Ref, it.Title)
	}
	if in.From == "" {
		in.From = defaultRef(r, repo)
	}
	setAudit(r, repo.Path, in.Name+" from "+in.From+" for "+it.Ref)
	sha, err := s.filesService().CreateBranch(r.Context(), repo.ID, in.Name, in.From, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	s.afterServerCommit(r.Context(), gitCaller{id: identityFrom(r), user: identityFrom(r).User}, repo, "refs/heads/"+in.Name, strings.Repeat("0", len(sha)), sha)
	link, err := s.work().LinkCode(r.Context(), c, it.Ref, work.CodeLink{Kind: work.CodeBranch, Repo: repo.Path, Ref: in.Name, Title: it.Title, Source: "manual"})
	if err != nil {
		// The branch exists; say so, and that the link was not made.
		writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "commit": sha, "item": it.Ref, "linked": false, "link_error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "commit": sha, "item": it.Ref, "linked": true, "link": link})
}

// itemOfBranch is the key of the item a branch was started for, when one is tied to it and the caller may see it.
func (s *Server) itemOfBranch(r *http.Request, repoPath, branch string) string {
	items, err := s.work().ItemsForCode(r.Context(), s.workCaller(r), work.CodeBranch, repoPath, branch)
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].Ref
}
