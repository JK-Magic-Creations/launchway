package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// Decision records (internal/work/decisions.go): reading one, answering it, replacing it, listing them, and writing
// the accepted ones into a repository's docs folder through a pull request.

func (s *Server) workDecisionRoutes() []route {
	const b = "/api/v1/work/items/{ref}/decision"
	return []route{
		{"GET " + b, signedIn, "", s.workDecisionGet},
		{"POST " + b + "/accept", signedIn, "work.decision_accept", s.workDecisionAccept},
		{"POST " + b + "/supersede", signedIn, "work.decision_supersede", s.workDecisionSupersede},
		{"GET /api/v1/work/decisions", signedIn, "", s.workDecisionList},
		{"POST /api/v1/repositories/{owner}/{name}/decisions/export", repoWrite, "repository.decisions_export", s.exportDecisions},
	}
}

func (s *Server) workDecisionGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.work().Decision(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workDecisionAccept(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Answer string `json:"answer"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.work().AcceptDecision(r.Context(), s.workCaller(r), r.PathValue("ref"), in.Answer)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, v.Item, v.Title)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workDecisionSupersede(w http.ResponseWriter, r *http.Request) {
	var in struct {
		By string `json:"by"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.work().SupersedeDecision(r.Context(), s.workCaller(r), r.PathValue("ref"), in.By)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, v.Item, "replaced by "+in.By)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workDecisionList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var scope *work.Scope
	if v := q.Get("scope"); v != "" {
		sc, err := parseWorkScope(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		scope = &sc
	}
	rows, err := s.work().ListDecisions(r.Context(), s.workCaller(r), scope, q.Get("repository"), q.Get("status"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

const defaultDecisionsFolder = "docs/decisions"

type exportDecisionsIn struct {
	Folder string `json:"folder"`
}

// exportDecisions writes the repository's accepted and superseded decisions that the caller may see as Markdown files
// under a folder (default docs/decisions) on a new branch, and opens a pull request for it. Files that are already
// there with the same text are left alone; when nothing differs, nothing is made.
func (s *Server) exportDecisions(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in exportDecisionsIn
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.Folder == "" {
		in.Folder = defaultDecisionsFolder
	}
	folder, err := files.CleanPath(in.Folder)
	if err != nil || folder == "" {
		writeErr(w, http.StatusBadRequest, "folder is a path inside the repository, such as docs/decisions")
		return
	}
	all, err := s.work().ExportDecisions(r.Context(), s.workCaller(r), repo.Path, folder)
	if err != nil {
		s.failWork(w, err)
		return
	}
	svc := s.filesService()
	base := repo.DefaultBranch
	up := files.Upload{Replace: true, Actor: identityFrom(r).User}
	var unchanged []string
	for _, f := range all {
		if cur, err := svc.Read(r.Context(), repo.ID, base, f.Path); err == nil && cur.Encoding == "utf8" && cur.Content == f.Content {
			unchanged = append(unchanged, f.Path)
			continue
		}
		up.Files = append(up.Files, files.UploadFile{Path: f.Path, Content: []byte(f.Content)})
	}
	if len(up.Files) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"changed": 0, "unchanged": unchanged, "message": "the decision records in " + folder + " are up to date; nothing was made"})
		return
	}
	up.Branch = "decisions-" + s.Now().UTC().Format("20060102-150405") + "-" + strings.TrimPrefix(ids.New("x"), "x")[:5]
	// Neither the message nor the pull request names an item: a reference in them would tie the pull request to the very
	// decisions it records, and the next export would find them changed.
	up.Message = fmt.Sprintf("Record %d decision(s) in %s", len(up.Files), folder)
	setAudit(r, repo.Path, up.Branch+" "+fmt.Sprint(len(up.Files))+" files")
	check := s.writePolicy(r.Context(), r, repo)
	sha, err := svc.CreateBranch(r.Context(), repo.ID, up.Branch, base, check)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	id := identityFrom(r)
	caller := gitCaller{id: id, user: id.User}
	s.afterServerCommit(r.Context(), caller, repo, "refs/heads/"+up.Branch, strings.Repeat("0", len(sha)), sha)
	out, err := svc.PutFiles(r.Context(), repo.ID, up, check)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	s.afterServerCommit(r.Context(), caller, repo, "refs/heads/"+out.Branch, out.Parent, out.Commit)
	body := fmt.Sprintf("Records %d decision(s) as Markdown files in `%s`, made from the decision records in Launchway; merging it keeps them with the code.", len(up.Files), folder)
	p, err := s.Forge.Pulls.Open(r.Context(), repo.ID, id.User, pulls.New{Title: fmt.Sprintf("Record %d decision(s) in %s", len(up.Files), folder), Body: body, SourceBranch: up.Branch, TargetBranch: base})
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]any{"changed": len(up.Files), "branch": up.Branch, "commit": out.Commit, "files": out.Paths, "unchanged": unchanged,
			"pull_error": "the files are on the branch, but the pull request could not be opened: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"changed": len(up.Files), "branch": up.Branch, "commit": out.Commit, "files": out.Paths, "unchanged": unchanged, "pull": p.Number})
}
