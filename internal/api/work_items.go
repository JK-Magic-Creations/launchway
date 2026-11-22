package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// The items of work: list, create, read, change, order, comment, link,
// attach. Who may see or change an item is the store's question (it knows
// scopes, confidential items and agents); the routes only carry it.

func (s *Server) workItemRoutes() []route {
	const b = "/api/v1/work/items"
	return []route{
		{"GET " + b, signedIn, "", s.workList},
		{"POST " + b, signedIn, "work.create", s.workCreate},
		{"GET " + b + "/{ref}", signedIn, "", s.workGet},
		{"PATCH " + b + "/{ref}", signedIn, "work.update", s.workUpdate},
		{"POST " + b + "/{ref}/rank", signedIn, "work.rank", s.workRank},
		{"POST " + b + "/{ref}/move", signedIn, "work.move", s.workMove},
		{"POST " + b + "/{ref}/take-over", signedIn, "work.take_over", s.workTakeOver},
		{"GET " + b + "/{ref}/history", signedIn, "", s.workHistory},
		{"GET " + b + "/{ref}/comments", signedIn, "", s.workComments},
		{"POST " + b + "/{ref}/comments", signedIn, "work.comment", s.workComment},
		{"PATCH /api/v1/work/comments/{id}", signedIn, "work.comment_edit", s.workEditComment},
		{"GET /api/v1/work/comments/{id}/edits", signedIn, "", s.workCommentEdits},
		{"GET " + b + "/{ref}/links", signedIn, "", s.workLinks},
		{"POST " + b + "/{ref}/links", signedIn, "work.link", s.workAddLink},
		{"DELETE /api/v1/work/links/{id}", signedIn, "work.unlink", s.workRemoveLink},
		{"GET " + b + "/{ref}/code", signedIn, "", s.workCodeLinks},
		{"POST " + b + "/{ref}/code", signedIn, "work.code_link", s.workAddCodeLink},
		{"GET " + b + "/{ref}/delivery", signedIn, "", s.workDelivery},
		{"GET " + b + "/{ref}/attachments", signedIn, "", s.workAttachments},
		{"POST " + b + "/{ref}/attachments", signedIn, "work.attach", s.workAttach},
		{"GET /api/v1/work/attachments/{id}", signedIn, "", s.workAttachment},
		{"GET /api/v1/work/views", signedIn, "", s.workViews},
		{"POST /api/v1/work/views", signedIn, "work.view_save", s.workSaveView},
		{"DELETE /api/v1/work/views/{id}", signedIn, "work.view_delete", s.workDeleteView},
		{"GET /api/v1/work/by-code", signedIn, "", s.workByCode},
		{"PUT " + b + "/{ref}/reactions/{reaction}", signedIn, "work.react", s.workReactItem},
		{"DELETE " + b + "/{ref}/reactions/{reaction}", signedIn, "work.react", s.workReactItem},
		{"PUT /api/v1/work/comments/{id}/reactions/{reaction}", signedIn, "work.react", s.workReactComment},
		{"DELETE /api/v1/work/comments/{id}/reactions/{reaction}", signedIn, "work.react", s.workReactComment},
	}
}

func (s *Server) workList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := work.ListOptions{Query: q.Get("q"), Sort: q.Get("sort"), Desc: q.Get("desc") == "true", Parent: q.Get("parent"), Repository: q.Get("repository")}
	o.Limit, _ = strconv.Atoi(q.Get("limit"))
	o.Offset, _ = strconv.Atoi(q.Get("offset"))
	if v := q.Get("scope"); v != "" {
		sc, err := parseWorkScope(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		o.Scope = &sc
	}
	pg, err := s.work().List(r.Context(), s.workCaller(r), o)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pg)
}

func (s *Server) workCreate(w http.ResponseWriter, r *http.Request) {
	var in work.NewItem
	if !decode(w, r, &in) {
		return
	}
	if in.Scope.Kind == "" && in.Repository != "" && r.URL.Query().Get("scope") == "" {
		in.Scope = work.Scope{Kind: work.ScopeProject, ID: in.Repository}
	}
	if in.Scope.Kind == "" {
		sc, err := parseWorkScope(r.URL.Query().Get("scope"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		in.Scope = sc
	}
	it, err := s.work().Create(r.Context(), s.workCaller(r), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, it.Title)
	if in.Repository != "" && !(it.Scope.Kind == work.ScopeProject && it.Scope.ID == in.Repository) {
		// The item lives elsewhere: tie it to the repository so the repository's list finds it.
		if _, err := s.work().LinkCode(r.Context(), s.workCaller(r), it.Ref, work.CodeLink{Kind: work.CodeRepository, Repo: in.Repository, Ref: in.Repository, Title: in.Repository, Source: "manual"}); err != nil {
			s.failWork(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, it)
}

// workDetail is an item with what hangs on it.
type workDetail struct {
	*work.Item
	Links       []work.Link       `json:"links"`
	Code        []work.CodeLink   `json:"code"`
	Attachments []work.Attachment `json:"attachments"`
	Delivery    *workDelivered    `json:"delivery,omitempty"`
	Usage       *work.UsageTotal  `json:"usage,omitempty"`
	Actions     []string          `json:"actions"`
}

func (s *Server) workGet(w http.ResponseWriter, r *http.Request) {
	c, ref := s.workCaller(r), r.PathValue("ref")
	st := s.work()
	it, err := st.Get(r.Context(), c, ref)
	if err != nil {
		s.failWork(w, err)
		return
	}
	d := workDetail{Item: it, Actions: []string{}}
	if d.Links, err = st.Links(r.Context(), c, ref); err == nil {
		d.Code, err = st.CodeLinks(r.Context(), c, ref)
	}
	if err == nil {
		d.Attachments, err = st.Attachments(r.Context(), c, ref)
	}
	if err != nil {
		s.failWork(w, err)
		return
	}
	d.Delivery = s.deliveryOf(r, it, d.Code)
	agent := ""
	if it.Claim != nil {
		agent = it.Claim.Agent
	}
	for _, a := range it.Assignees {
		if a.Kind == work.KindAgent && agent == "" {
			agent = a.Name
		}
	}
	if agent != "" {
		d.Usage, _ = st.ItemUsage(r.Context(), it.ID, agent)
	}
	if c.Write != nil && c.Write(it.Scope) {
		d.Actions = append(d.Actions, "edit", "comment", "move", "link")
	}
	if c.Maintain != nil && c.Maintain(it.Scope) {
		d.Actions = append(d.Actions, "rank", "maintain")
	}
	if c.Write != nil && c.Write(it.Scope) && agent != "" && c.Human() {
		d.Actions = append(d.Actions, "take_over")
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) workUpdate(w http.ResponseWriter, r *http.Request) {
	var p work.Patch
	if !decode(w, r, &p) {
		return
	}
	it, err := s.work().Update(r.Context(), s.workCaller(r), r.PathValue("ref"), p)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, it.Title)
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) workRank(w http.ResponseWriter, r *http.Request) {
	var p work.Position
	if !decode(w, r, &p) {
		return
	}
	it, err := s.work().Rank(r.Context(), s.workCaller(r), r.PathValue("ref"), p)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, p.Where+" "+p.Other)
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) workMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State  string `json:"state"`
		Before string `json:"before"`
	}
	if !decode(w, r, &in) {
		return
	}
	it, err := s.work().Move(r.Context(), s.workCaller(r), r.PathValue("ref"), in.State, in.Before)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, "to "+it.State)
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) workTakeOver(w http.ResponseWriter, r *http.Request) {
	it, err := s.work().TakeOver(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, "taken over from an agent")
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) workHistory(w http.ResponseWriter, r *http.Request) {
	c := s.workCaller(r)
	it, err := s.work().Get(r.Context(), c, r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ev, err := s.work().History(r.Context(), it.ID, after, limit)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it.Ref, "events": ev})
}

func (s *Server) workComments(w http.ResponseWriter, r *http.Request) {
	cs, err := s.work().Comments(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": cs})
}

func (s *Server) workComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	cm, err := s.work().AddComment(r.Context(), s.workCaller(r), r.PathValue("ref"), in.Body)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, cm.ItemRef, "comment")
	writeJSON(w, http.StatusCreated, cm)
}

func (s *Server) workEditComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	cm, err := s.work().EditComment(r.Context(), s.workCaller(r), r.PathValue("id"), in.Body)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), "comment edited")
	writeJSON(w, http.StatusOK, cm)
}

func (s *Server) workCommentEdits(w http.ResponseWriter, r *http.Request) {
	es, err := s.work().CommentEdits(r.Context(), s.workCaller(r), r.PathValue("id"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"edits": es})
}

func (s *Server) workLinks(w http.ResponseWriter, r *http.Request) {
	ls, err := s.work().Links(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": ls})
}

func (s *Server) workAddLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
		To   string `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	l, err := s.work().AddLink(r.Context(), s.workCaller(r), r.PathValue("ref"), in.Kind, in.To)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, l.From, l.Kind+" "+l.To)
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) workRemoveLink(w http.ResponseWriter, r *http.Request) {
	if err := s.work().RemoveLink(r.Context(), s.workCaller(r), r.PathValue("id")); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), "")
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

func (s *Server) workCodeLinks(w http.ResponseWriter, r *http.Request) {
	ls, err := s.work().CodeLinks(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": ls})
}

func (s *Server) workAddCodeLink(w http.ResponseWriter, r *http.Request) {
	var in work.CodeLink
	if !decode(w, r, &in) {
		return
	}
	c := s.workCaller(r)
	if c.Agent != nil {
		in.Source = "agent"
	} else if in.Source == "" {
		in.Source = "manual"
	}
	l, err := s.work().LinkCode(r.Context(), c, r.PathValue("ref"), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("ref"), l.Kind+" "+l.Repo+" "+l.Ref)
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) workAttachments(w http.ResponseWriter, r *http.Request) {
	as, err := s.work().Attachments(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": as})
}

// workAttach takes a file as the request body, named by ?name= (so a script
// can use curl --data-binary), with its kind in ?kind=.
func (s *Server) workAttach(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data, err := io.ReadAll(io.LimitReader(r.Body, work.MaxAttachment+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the file could not be read")
		return
	}
	a, err := s.work().AddAttachment(r.Context(), s.workCaller(r), r.PathValue("ref"), q.Get("name"), r.Header.Get("Content-Type"), q.Get("kind"), data)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("ref"), a.Name)
	writeJSON(w, http.StatusCreated, a)
}

// workInlineTypes are the only types a browser is told to show in the page; the
// rest download. Nothing here is ever served as a page, a script or a style.
var workInlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "text/plain": true}

// workAttachment serves a file the way a repository's raw files are served:
// as a download, sniffing off, scripts forbidden, and only a short list of
// harmless types shown inline.
func (s *Server) workAttachment(w http.ResponseWriter, r *http.Request) {
	a, data, err := s.work().AttachmentData(r.Context(), s.workCaller(r), r.PathValue("id"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(a.ContentType, ";", 2)[0]))
	disp := "attachment"
	if workInlineTypes[ct] && r.URL.Query().Get("download") == "" {
		disp = "inline"
	} else {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+urlPathEscape(a.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=0")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)+256, 16)[1:]))
		}
	}
	return b.String()
}

func (s *Server) workViews(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	vs, err := s.work().Views(r.Context(), s.workCaller(r), sc)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": vs})
}

func (s *Server) workSaveView(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope  string `json:"scope"`
		Name   string `json:"name"`
		Query  string `json:"query"`
		Shared bool   `json:"shared"`
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := s.work().SaveView(r.Context(), s.workCaller(r), sc, in.Name, in.Query, in.Shared)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), v.Name)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) workDeleteView(w http.ResponseWriter, r *http.Request) {
	if err := s.work().DeleteView(r.Context(), s.workCaller(r), r.PathValue("id")); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), "")
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

// workByCode finds the items tied to a commit or a pull request.
func (s *Server) workByCode(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := s.work().ItemsForCode(r.Context(), s.workCaller(r), q.Get("kind"), q.Get("repo"), q.Get("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

var _ = time.Second

// workReactItem adds (PUT) or removes (DELETE) the caller's reaction to an item.
func (s *Server) workReactItem(w http.ResponseWriter, r *http.Request) {
	out, err := s.work().React(r.Context(), s.workCaller(r), false, r.PathValue("ref"), r.PathValue("reaction"), r.Method == http.MethodPut)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("ref"), r.PathValue("reaction"))
	writeJSON(w, http.StatusOK, map[string]any{"reactions": out})
}

func (s *Server) workReactComment(w http.ResponseWriter, r *http.Request) {
	out, err := s.work().React(r.Context(), s.workCaller(r), true, r.PathValue("id"), r.PathValue("reaction"), r.Method == http.MethodPut)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), r.PathValue("reaction"))
	writeJSON(w, http.StatusOK, map[string]any{"reactions": out})
}
