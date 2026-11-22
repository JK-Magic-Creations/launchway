package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
)

// What a hosted repository holds, read and written without a clone: its
// commit log, a directory or a file at a ref, and one commit that adds or
// changes one file. Reading is for the repository's readers. Writing is for its
// operators, goes through the same branch rules and the same follow-up as a
// push (webhooks, push mirrors, pull requests), and is audited.

func (s *Server) filesRoutes() []route {
	const base = "/api/v1/repositories/{owner}/{name}"
	return append(s.browseRoutes(), []route{
		{"GET " + base + "/commits", repoRead, "", s.listCommits},
		{"GET " + base + "/files", repoRead, "", s.readFile},
		{"PUT " + base + "/files", repoWrite, "repository.file_write", s.writeFile},
		{"DELETE " + base + "/files", repoWrite, "repository.file_delete", s.deleteFile},
		{"POST " + base + "/files", repoWrite, "repository.files_upload", s.uploadFiles},
	}...)
}

// filesService reads and writes through the storage the pull request service
// already holds (nil when pull requests, and with them this, are off).
func (s *Server) filesService() *files.Service {
	if s.Forge == nil || s.Forge.Pulls == nil {
		return nil
	}
	return &files.Service{Git: s.Forge.Pulls.Git}
}

func (s *Server) filesRepo(w http.ResponseWriter, r *http.Request) (forge.Repository, bool) {
	if !s.forgeOn(w) {
		return forge.Repository{}, false
	}
	if s.filesService() == nil {
		writeFeatureOff(w)
		return forge.Repository{}, false
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return repo, false
	}
	return repo, true
}

// refusedByRules is a change the branch rules turned down, with the message a push of it would get.
type refusedByRules struct{ error }

func (e refusedByRules) Unwrap() error { return e.error }

func (s *Server) failFiles(w http.ResponseWriter, err error) {
	var refused refusedByRules
	switch {
	case errors.As(err, &refused):
		writeErr(w, http.StatusForbidden, refused.Error())
	case errors.Is(err, files.ErrConflict):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "conflict: "))
	case errors.Is(err, forge.ErrNotFound):
		writeErr(w, http.StatusNotFound, strings.TrimPrefix(err.Error(), "not found: "))
	case errors.Is(err, forge.ErrInvalid):
		s.failForge(w, err)
	default:
		if files.IsStale(err) {
			writeErr(w, http.StatusConflict, "the branch moved while the commit was being made: read it again and retry")
			return
		}
		s.failForge(w, err)
	}
}

// defaultRef is the ref a request names, or the repository's default branch.
func defaultRef(r *http.Request, repo forge.Repository) string {
	if v := r.URL.Query().Get("ref"); v != "" {
		return v
	}
	if repo.DefaultBranch != "" {
		return repo.DefaultBranch
	}
	return "main"
}

func (s *Server) listCommits(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cs, err := s.filesService().Commits(r.Context(), repo.ID, defaultRef(r, repo), r.URL.Query().Get("path"), limit)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cs, "ref": defaultRef(r, repo)})
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	read := s.filesService().Read
	if r.URL.Query().Get("last") == "1" {
		read = s.filesService().ReadWithLast
	}
	it, err := read(r.Context(), repo.ID, defaultRef(r, repo), r.URL.Query().Get("path"))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	cacheFor(w, defaultRef(r, repo))
	writeJSON(w, http.StatusOK, it)
}

type writeFileIn struct {
	Branch      string `json:"branch"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"` // utf8 (the default) or base64
	Message     string `json:"message"`
	ExpectedSHA string `json:"expected_sha"`
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) { s.changeFile(w, r, true) }

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) { s.changeFile(w, r, false) }

func (s *Server) changeFile(w http.ResponseWriter, r *http.Request, remove bool) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	var in writeFileIn
	if !decode(w, r, &in) {
		return
	}
	if repo.Mirror || repo.Archived {
		writeErr(w, http.StatusConflict, repo.Path+" takes no changes: it is "+map[bool]string{true: "a mirror", false: "archived"}[repo.Mirror])
		return
	}
	var content []byte
	switch in.Encoding {
	case "", "utf8":
		content = []byte(in.Content)
	case "base64":
		b, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "content is not valid base64")
			return
		}
		content = b
	default:
		writeErr(w, http.StatusBadRequest, "encoding is utf8 or base64")
		return
	}
	if in.Branch == "" {
		in.Branch = defaultRef(r, repo)
	}
	id := identityFrom(r)
	setAudit(r, repo.Path, in.Branch+" "+in.Path)
	if s.Forge.Rules == nil {
		writeErr(w, http.StatusServiceUnavailable, "branch rules are not available; nothing was changed")
		return
	}
	caller := gitCaller{id: id, user: id.User}
	policy := s.pushPolicy(caller, repo)
	ctx := r.Context()
	check := func(ref, old, next string) error {
		if policy == nil {
			return nil
		}
		if err := policy(ctx, []githttp.RefUpdate{{Name: ref, Old: old, New: next}}); err != nil {
			return refusedByRules{err}
		}
		return nil
	}
	out, err := s.filesService().Put(r.Context(), repo.ID, files.Write{Branch: in.Branch, Path: in.Path, Content: content, Message: in.Message, ExpectedSHA: in.ExpectedSHA, Actor: id.User, Delete: remove}, check)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", in.Branch+" "+out.Path+" "+short(out.Parent)+".."+short(out.Commit))
	s.afterServerCommit(r.Context(), caller, repo, "refs/heads/"+out.Branch, out.Parent, out.Commit)
	writeJSON(w, http.StatusOK, out)
}

// afterServerCommit tells the rest of the system about a commit the server made
// on someone's behalf, as it would be told of a push.
func (s *Server) afterServerCommit(ctx context.Context, c gitCaller, repo forge.Repository, ref, old, next string) {
	if s.Forge.Mirrors != nil {
		s.Forge.Mirrors.Pushed(ctx, repo.ID)
	}
	s.afterPush(ctx, c, repo, []githttp.RefUpdate{{Name: ref, Old: old, New: next}})
}

type uploadIn struct {
	Branch  string       `json:"branch"`
	Message string       `json:"message"`
	Replace bool         `json:"replace"`
	Files   []uploadFile `json:"files"`
}

// uploadFile is one file of an upload; encoding is utf8 or base64 (the default: uploads are bytes).
type uploadFile struct {
	Path     string `json:"path" jsonschema:"the file's path in the repository"`
	Content  string `json:"content" jsonschema:"the file's content, base64 unless encoding is utf8"`
	Encoding string `json:"encoding,omitempty" jsonschema:"base64 (default) or utf8"`
}

// uploadFiles adds several files in one commit, on a branch the caller may push to: the rules of a push apply.
func (s *Server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.writableRepo(w, r)
	if !ok {
		return
	}
	var in uploadIn
	if !decode(w, r, &in) {
		return
	}
	if in.Branch == "" {
		in.Branch = defaultRef(r, repo)
	}
	up := files.Upload{Branch: in.Branch, Message: in.Message, Replace: in.Replace, Actor: identityFrom(r).User}
	for _, f := range in.Files {
		var b []byte
		switch f.Encoding {
		case "utf8":
			b = []byte(f.Content)
		case "", "base64":
			d, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				writeErr(w, http.StatusBadRequest, f.Path+": content is not valid base64")
				return
			}
			b = d
		default:
			writeErr(w, http.StatusBadRequest, "encoding is base64 (the default) or utf8")
			return
		}
		up.Files = append(up.Files, files.UploadFile{Path: f.Path, Content: b})
	}
	setAudit(r, repo.Path, in.Branch+" "+strconv.Itoa(len(up.Files))+" files")
	out, err := s.filesService().PutFiles(r.Context(), repo.ID, up, s.writePolicy(r.Context(), r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	setAudit(r, "", in.Branch+" "+strings.Join(out.Paths, ", ")+" "+short(out.Parent)+".."+short(out.Commit))
	id := identityFrom(r)
	s.afterServerCommit(r.Context(), gitCaller{id: id, user: id.User}, repo, "refs/heads/"+out.Branch, out.Parent, out.Commit)
	writeJSON(w, http.StatusCreated, out)
}
