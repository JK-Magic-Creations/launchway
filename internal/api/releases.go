package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/release"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Releases (schema 46; see engine/releases.go and internal/release). A release
// is the unit that is versioned, approved, promoted through Airfields and
// reported on. Who may do what:
//
//   - a viewer reads published and withdrawn releases, their notes, assets
//     (when signed in), the matrix and the report;
//   - an operator makes drafts, edits notes, adds files, makes candidates,
//     publishes, promotes (where the Airfield's own rules allow), gives
//     clearance (as the checklist's roles say) and withdraws releases, and sees drafts;
//   - an administrator of the project sets its release settings and
//     checklists, makes freezes of the project, overrides a checklist line
//     that may be overridden (a window, a soak time or a freeze) with a
//     reason, and may make a release public when the installation allows it;
//   - an administrator of every project makes freezes of every project and
//     says whether the installation allows public downloads.

// ReleaseOptions are what a server is given for releases: the work tracker's source of changes, when there is one
// (nil: merged pull requests and commits), and how a promotion starts its Sortie (nil: the project manager does).
type ReleaseOptions struct {
	Notes release.Source
	Start func(ctx context.Context, rq source.StartRequest) (*engine.RunSummary, error)
}

func (s *Server) releaseRoutes() []route {
	p := "/api/v1/projects/{name}"
	return []route{
		{"GET " + p + "/release-settings", projectRead, "", s.getReleaseSettings},
		{"PUT " + p + "/release-settings", projectOperate, "release.settings", s.putReleaseSettings},
		{"GET " + p + "/releases", projectRead, "", s.listProjectReleases},
		{"POST " + p + "/releases", projectOperate, "release.create", s.createRelease},
		{"GET " + p + "/releases/next", projectRead, "", s.nextVersion},
		{"GET " + p + "/releases/notes", projectRead, "", s.previewNotes},
		{"GET " + p + "/releases/compare", projectRead, "", s.compareReleases},
		{"GET " + p + "/releases/{version}", projectRead, "", s.getRelease},
		{"PATCH " + p + "/releases/{version}", projectOperate, "release.edit", s.editRelease},
		{"DELETE " + p + "/releases/{version}", projectOperate, "release.delete", s.deleteRelease},
		{"POST " + p + "/releases/{version}/notes", projectOperate, "release.notes", s.regenerateNotes},
		{"PUT " + p + "/releases/{version}/assets/{asset}", projectOperate, "release.asset.add", s.uploadReleaseAsset},
		{"POST " + p + "/releases/{version}/assets", projectOperate, "release.asset.add", s.addReleaseAssets},
		{"DELETE " + p + "/releases/{version}/assets/{asset}", projectOperate, "release.asset.remove", s.removeReleaseAsset},
		{"GET " + p + "/releases/{version}/assets/{asset}", projectRead, "release.asset.download", s.downloadReleaseAsset},
		{"POST " + p + "/releases/{version}/candidate", projectOperate, "release.candidate", s.makeCandidate},
		{"POST " + p + "/releases/{version}/publish", projectOperate, "release.publish", s.publishRelease},
		{"POST " + p + "/releases/{version}/yank", projectOperate, "release.yank", s.yankRelease},
		{"POST " + p + "/releases/{version}/outside/{mirror}/retry", projectOperate, "release.outside_retry", s.retryOutside},
		{"POST " + p + "/releases/{version}/changelog", projectOperate, "release.changelog", s.proposeChangelog},
		{"GET /public/releases/{name}/{version}/{asset}", public, "release.asset.download", s.publicDownload},
	}
}

// ---- helpers ----

func (s *Server) roleName(id *identity, project string) string {
	if s.Auth == nil {
		return "admin"
	}
	switch s.roleOn(id, project) {
	case auth.Admin:
		return "admin"
	case auth.Operator:
		return "operator"
	}
	return "viewer"
}

func (s *Server) isProjectAdmin(id *identity, project string) bool {
	return s.authorize(id, auth.AdministerProject, project)
}

// releaseOf finds the release a request names (a version, a tag or an id) and
// hides drafts from those who may not see them.
func (s *Server) releaseOf(w http.ResponseWriter, r *http.Request) (*engine.Release, bool) {
	project, v := r.PathValue("name"), r.PathValue("version")
	var rel *engine.Release
	var err error
	if strings.HasPrefix(v, "rel_") {
		if rel, err = s.Engine.GetRelease(v); err == nil && rel.Project != project {
			err = fmt.Errorf("%w: %s has no release %s", engine.ErrNotFound, project, v)
		}
	} else if v == "latest" && r.Method == http.MethodGet {
		rel, err = s.Engine.LatestRelease(project) // the stable address of the newest published release that is not a pre-release
	} else {
		rel, err = s.Engine.FindRelease(project, v)
	}
	if err == nil && rel.State == engine.ReleaseDraft && !s.authorize(identityFrom(r), auth.Operate, project) {
		err = fmt.Errorf("%w: %s has no release %s", engine.ErrNotFound, project, v)
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return rel, true
}

func releaseTarget(rel *engine.Release) string { return rel.Project + " " + rel.Version }

func (s *Server) releaseView(r *http.Request, rel *engine.Release) map[string]any {
	set, _ := s.Engine.ReleaseSettingsOf(rel.Project)
	out := map[string]any{"release": rel, "settings": set}
	proms, _ := s.Engine.ReleasePromotions(rel.ID, "", 50)
	out["promotions"] = proms
	if ck, err := s.Engine.ReleaseClearances(rel.ID); err == nil {
		out["clearances"] = ck
	}
	out["links"] = s.releaseLinks(r, rel)
	out["outside"] = s.outsideViews(r, rel)
	id := identityFrom(r)
	out["may"] = map[string]bool{
		"edit": s.authorize(id, auth.Operate, rel.Project) && !rel.Immutable, "publish": s.authorize(id, auth.Operate, rel.Project) && (rel.State == engine.ReleaseDraft || rel.State == engine.ReleaseCandidate),
		"promote":       s.authorize(id, auth.Operate, rel.Project) && (rel.State == engine.ReleasePublished || rel.State == engine.ReleaseCandidate),
		"yank":          s.authorize(id, auth.Operate, rel.Project) && (rel.State == engine.ReleasePublished || rel.State == engine.ReleaseCandidate),
		"administer":    s.isProjectAdmin(id, rel.Project),
		"outside_retry": s.authorize(id, auth.Operate, rel.Project) && rel.State == engine.ReleasePublished,
	}
	return out
}

func (s *Server) releaseLinks(r *http.Request, rel *engine.Release) map[string]string {
	l := map[string]string{"page": s.baseURL(r) + "/deployments/releases/" + rel.Project + "/" + rel.Version}
	if rel.Tag != "" && s.Projects != nil {
		if u := s.Projects.ProjectRepoURL(rel.Project); u != "" {
			l["repository"] = u
			// A repository hosted here has verified commits and tags: the page shows their badge by this path.
			if _, path, ok := s.Resolve(r.Context(), u); ok {
				l["hosted_repository"] = path
			}
		}
	}
	return l
}

// ---- settings ----

func (s *Server) getReleaseSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.Engine.ReleaseSettingsOf(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	allowed, _ := s.Engine.PublicReleasesAllowed()
	writeJSON(w, http.StatusOK, map[string]any{"settings": set, "installation_allows_public": allowed,
		"writable": s.canTag(r.Context(), set.Project), "schemes": release.Schemes})
}

func (s *Server) putReleaseSettings(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	id := identityFrom(r)
	if !s.isProjectAdmin(id, project) {
		writeErr(w, http.StatusForbidden, "you need the admin role on "+project+" to change how its releases are named, ordered and made public")
		return
	}
	var in engine.ReleaseSettings
	if !decode(w, r, &in) {
		return
	}
	in.Project = project
	if in.Public {
		if ok, _ := s.Engine.PublicReleasesAllowed(); !ok {
			writeErr(w, http.StatusConflict, "this installation does not allow public downloads; an administrator of every {project} turns that on in the release settings of the installation first")
			return
		}
	}
	setAudit(r, project, fmt.Sprintf("scheme %s, tag prefix %q, public %v", in.Scheme, in.TagPrefix, in.Public))
	set, err := s.Engine.SetReleaseSettings(in, id.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// ---- reading ----

func (s *Server) listProjectReleases(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	id := identityFrom(r)
	list, err := s.Engine.ListReleases(engine.ReleaseFilter{Project: project, State: r.URL.Query().Get("state")})
	if err != nil {
		s.fail(w, err)
		return
	}
	mayDraft := s.authorize(id, auth.Operate, project)
	out := list[:0]
	for _, x := range list {
		if x.State == engine.ReleaseDraft && !mayDraft {
			continue
		}
		x.Notes = "" // the list is small; the notes are on the release
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	v := s.releaseView(r, rel)
	// A tag that moved is a warning: a published release's tag never moves.
	if rel.Tag != "" && rel.TagObject != "" && s.Projects != nil {
		if ti, err := s.Projects.ReleaseTag(r.Context(), rel.Project, rel.Tag); err == nil && (!ti.Exists || ti.Commit != rel.SHA) {
			v["warnings"] = []string{"the tag " + rel.Tag + " no longer points to " + rel.SHA[:min(8, len(rel.SHA))] + " in the repository; the release still names the commit it was published at, and promotions refuse until the tag is back"}
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- creating ----

type createReleaseIn struct {
	Version string `json:"version"`
	Title   string `json:"title"`
	// From is the branch, tag or commit to release (default: the default branch).
	From string `json:"from"`
	// Adopt names an existing tag to release; the release takes its commit.
	Adopt string `json:"adopt"`
	Notes string `json:"notes"`
	// GenerateNotes writes the notes from the changes since the previous release.
	GenerateNotes bool   `json:"generate_notes"`
	RunID         string `json:"run_id"`
	AllowLower    bool   `json:"allow_lower"`
	// AttachCargo copies every file the Sortie kept into the release, and records its image digests (RunID, or the
	// latest successful Sortie at the release's commit when none is named).
	AttachCargo bool `json:"attach_cargo"`
	// Publish makes the tag and fixes the release in the same request; Public asks for downloads without signing in.
	Publish bool `json:"publish"`
	Public  bool `json:"public"`
}

func (s *Server) createRelease(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	project := r.PathValue("name")
	var in createReleaseIn
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, project+" "+in.Version, "from "+nonEmpty(in.Adopt, nonEmpty(in.From, "the default branch")))
	set, err := s.Engine.ReleaseSettingsOf(project)
	if err != nil {
		s.fail(w, err)
		return
	}
	if in.Version == "" {
		writeErr(w, http.StatusBadRequest, "say which version to release: {\"version\": \"1.4.0\"}; launchway release next proposes one")
		return
	}
	if in.AllowLower && !s.isProjectAdmin(identityFrom(r), project) {
		// Anyone who may release may release from a maintenance branch; only the project's
		// administrators may release a lower version of the same line out of order, so say so.
		in.AllowLower = s.authorize(identityFrom(r), auth.Operate, project)
	}
	n := engine.NewRelease{Project: project, Version: in.Version, Title: in.Title, Notes: in.Notes, RunID: in.RunID, AllowLower: in.AllowLower, Actor: identityFrom(r).User}
	if in.Adopt != "" {
		ti, err := s.Projects.ReleaseTag(r.Context(), project, in.Adopt)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !ti.Exists {
			writeErr(w, http.StatusNotFound, "there is no tag "+in.Adopt+" in "+project+" to adopt")
			return
		}
		n.SHA, n.Tag, n.TagObject = ti.Commit, ti.Name, ti.Object
		if !ti.Annotated {
			writeErr(w, http.StatusConflict, "the tag "+in.Adopt+" is a lightweight tag; a release adopts an annotated tag, which says who made it and when. Make it annotated (git tag -a) and adopt it, or let Launchway create the tag")
			return
		}
		ref, err := s.Projects.ResolveRef(r.Context(), project, in.Adopt)
		if err != nil {
			s.fail(w, err)
			return
		}
		n.CommitTime, n.Branch = ref.CommittedAt, ""
	} else {
		ref, err := s.Projects.ResolveRef(r.Context(), project, in.From)
		if err != nil {
			s.fail(w, err)
			return
		}
		n.SHA, n.CommitTime = ref.SHA, ref.CommittedAt
		if ref.Kind == "branch" {
			n.Branch = ref.Name
		}
	}
	if in.GenerateNotes && n.Notes == "" {
		md, src, err := s.generateNotes(r.Context(), project, set, in.Version, n.SHA)
		if err != nil {
			s.fail(w, err)
			return
		}
		n.Notes, n.NotesSource = md, src
	}
	rel, err := s.Engine.CreateRelease(n)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, releaseTarget(rel), "draft at "+rel.SHA[:8])
	if in.AttachCargo {
		if err := s.attachCargoOf(r.Context(), rel, in.RunID, identityFrom(r).User); err != nil {
			s.releaseHalfDone(w, rel, "attaching the files failed", err)
			return
		}
	}
	if in.Publish {
		got, _, msg := s.publishCore(r, rel, in.Public, "")
		if got == nil {
			s.releaseHalfDone(w, rel, "publishing failed", errors.New(msg))
			return
		}
		rel = got
		setAudit(r, releaseTarget(rel), "cut: draft at "+rel.SHA[:8]+", published as tag "+rel.Tag)
	}
	if fresh, err := s.Engine.FindRelease(rel.Project, rel.Version); err == nil {
		rel = fresh
	}
	writeJSON(w, http.StatusCreated, s.releaseView(r, rel))
}

// ---- changes, notes and the next version ----

// releaseChanges reads the changes between the previous release's commit and a commit: from the
// work tracker's source when there is one, else from merged pull requests and commits.
func (s *Server) releaseChanges(ctx context.Context, project, fromSHA, toSHA string) ([]release.Change, string, error) {
	if s.Releases.Notes != nil {
		if ch, err := s.Releases.Notes.ChangesSince(project, fromSHA, toSHA); err == nil {
			return ch, s.Releases.Notes.Name(), nil
		}
	}
	commits, err := s.Projects.CommitsBetween(ctx, project, fromSHA, toSHA)
	if err != nil {
		return nil, "", err
	}
	var repoPath string
	var repoID string
	if s.Forge != nil && s.Forge.Pulls != nil {
		if repo, err := s.Projects.ForgeRepository(ctx, project); err == nil {
			repoPath, repoID = repo.Path, repo.ID
		}
	}
	base := ""
	if repoPath != "" {
		base = "/repositories/" + repoPath + "/pulls/"
	}
	ch := source.ChangesFromCommits(commits, func(n int) string {
		if base == "" {
			return ""
		}
		return base + strconv.Itoa(n)
	})
	// Labels, titles and authors of merged pull requests of a hosted repository.
	if repoID != "" {
		merged, _, err := s.Forge.Pulls.Store.List(ctx, repoID, pulls.Filter{State: "merged", Limit: 500})
		if err == nil {
			by := map[int]pulls.PullRequest{}
			for _, p := range merged {
				by[p.Number] = p
			}
			for i := range ch {
				if p, ok := by[ch[i].Ref]; ok && ch[i].Kind == "pull" {
					ch[i].Labels, ch[i].Title, ch[i].Author = p.Labels, p.Title, nonEmpty(p.Author, ch[i].Author)
					if p.Body != "" && ch[i].Body == "" {
						ch[i].Body = p.Body
					}
				}
			}
		}
	}
	return ch, "merged pull requests and commits", nil
}

// previousOf is the published release before a version, with its commit.
func (s *Server) previousOf(project string, set engine.ReleaseSettings, version string) *engine.Release {
	list, err := s.Engine.ListReleases(engine.ReleaseFilter{Project: project})
	if err != nil {
		return nil
	}
	var prev *engine.Release
	for i := range list {
		x := &list[i]
		if x.State != engine.ReleasePublished && x.State != engine.ReleaseYanked {
			continue
		}
		if version != "" && set.Scheme != release.SchemeCreated && release.Compare(set.Scheme, x.Version, version) >= 0 {
			continue
		}
		if prev == nil || (set.Scheme == release.SchemeCreated && x.CreatedAt > prev.CreatedAt) || (set.Scheme != release.SchemeCreated && release.Compare(set.Scheme, x.Version, prev.Version) > 0) {
			prev = x
		}
	}
	return prev
}

func (s *Server) generateNotes(ctx context.Context, project string, set engine.ReleaseSettings, version, sha string) (string, string, error) {
	from := ""
	if prev := s.previousOf(project, set, version); prev != nil {
		from = prev.SHA
	}
	ch, src, err := s.releaseChanges(ctx, project, from, sha)
	if err != nil {
		return "", "", err
	}
	return release.Render(version, ch, src).Markdown, "generated from " + src, nil
}

func (s *Server) previewNotes(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	project, q := r.PathValue("name"), r.URL.Query()
	set, err := s.Engine.ReleaseSettingsOf(project)
	if err != nil {
		s.fail(w, err)
		return
	}
	ref, err := s.Projects.ResolveRef(r.Context(), project, q.Get("to"))
	if err != nil {
		s.fail(w, err)
		return
	}
	version := nonEmpty(q.Get("version"), "Unreleased")
	from := ""
	if prev := s.previousOf(project, set, q.Get("version")); prev != nil {
		from = prev.SHA
	}
	ch, src, err := s.releaseChanges(r.Context(), project, from, ref.SHA)
	if err != nil {
		s.fail(w, err)
		return
	}
	n := release.Render(version, ch, src)
	writeJSON(w, http.StatusOK, map[string]any{"notes": n, "from": from, "to": ref.SHA})
}

func (s *Server) nextVersion(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	project, q := r.PathValue("name"), r.URL.Query()
	set, err := s.Engine.ReleaseSettingsOf(project)
	if err != nil {
		s.fail(w, err)
		return
	}
	ref, err := s.Projects.ResolveRef(r.Context(), project, q.Get("from"))
	if err != nil {
		s.fail(w, err)
		return
	}
	hi, _, err := s.Engine.HighestRelease(project)
	if err != nil {
		s.fail(w, err)
		return
	}
	from := ""
	last := hi
	if prev := s.previousOf(project, set, ""); prev != nil {
		from, last = prev.SHA, prev.Version
	}
	ch, src, err := s.releaseChanges(r.Context(), project, from, ref.SHA)
	if err != nil {
		s.fail(w, err)
		return
	}
	p := release.Propose(set.Scheme, last, ch)
	if set.Scheme == release.SchemeCalver && len(ch) > 0 {
		now := s.Now().UTC()
		p.Next = fmt.Sprintf("%d.%d.%d", now.Year(), int(now.Month()), now.Day())
		p.Summary = "A calendar version is today's date: " + p.Next + "."
	}
	if pre := q.Get("pre"); pre != "" && set.Scheme == release.SchemeSemver && p.Next != "" {
		var existing []string
		if list, err := s.Engine.ListReleases(engine.ReleaseFilter{Project: project}); err == nil {
			for _, x := range list {
				existing = append(existing, x.Version)
			}
		}
		p.Next = release.NextPre(p.Next, pre, existing)
		p.Summary += " As a pre-release: " + p.Next + "."
	}
	if p.Next != "" {
		if err := release.MayRelease(set.Scheme, hi, p.Next, false); err != nil {
			p.Warnings = append(p.Warnings, err.Error())
		}
	}
	branch := ""
	if ref.Kind == "branch" {
		branch = ref.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": p, "source": src, "commit": ref.SHA, "branch": branch, "highest": hi, "scheme": set.Scheme, "tag_prefix": set.TagPrefix,
		"hosted": s.isHosted(r.Context(), project), "can_tag": s.canTag(r.Context(), project), "build": s.buildAt(project, ref.SHA),
		"note": "a person confirms or overrides this: pass the version you choose to create a release"})
}

func (s *Server) compareReleases(w http.ResponseWriter, r *http.Request) {
	project, q := r.PathValue("name"), r.URL.Query()
	a, err := s.Engine.FindRelease(project, q.Get("a"))
	if err != nil {
		s.fail(w, err)
		return
	}
	b, err := s.Engine.FindRelease(project, q.Get("b"))
	if err != nil {
		s.fail(w, err)
		return
	}
	id := identityFrom(r)
	for _, x := range []*engine.Release{a, b} {
		if x.State == engine.ReleaseDraft && !s.authorize(id, auth.Operate, project) {
			writeErr(w, http.StatusNotFound, "no release "+x.Version)
			return
		}
	}
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	// Order them so that the commits listed are those the newer has and the older lacks.
	set, _ := s.Engine.ReleaseSettingsOf(project)
	older, newer := a, b
	if release.Compare(set.Scheme, a.Version, b.Version) > 0 || (set.Scheme == release.SchemeCreated && a.CreatedAt > b.CreatedAt) {
		older, newer = b, a
	}
	ch, src, err := s.releaseChanges(r.Context(), project, older.SHA, newer.SHA)
	if err != nil {
		s.fail(w, err)
		return
	}
	diff, err := s.Projects.Compare(r.Context(), project, older.SHA, newer.SHA)
	if err != nil {
		s.fail(w, err)
		return
	}
	n := release.Render(newer.Version, ch, src)
	writeJSON(w, http.StatusOK, map[string]any{"older": older.Version, "newer": newer.Version, "changes": ch, "notes": n.Markdown, "files": diff,
		"digests": map[string]any{"older": older.Digests, "newer": newer.Digests}, "source": src})
}

// ---- editing and states ----

func (s *Server) editRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Title *string `json:"title"`
		Notes *string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, releaseTarget(rel), "edited the notes or title")
	got, err := s.Engine.EditRelease(rel.ID, in.Title, in.Notes)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.releaseView(r, got))
}

func (s *Server) regenerateNotes(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	set, _ := s.Engine.ReleaseSettingsOf(rel.Project)
	md, src, err := s.generateNotes(r.Context(), rel.Project, set, rel.Version, rel.SHA)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, releaseTarget(rel), src)
	if err := s.Engine.SetReleaseNotes(rel.ID, md, src); err != nil {
		s.fail(w, err)
		return
	}
	got, _ := s.Engine.GetRelease(rel.ID)
	writeJSON(w, http.StatusOK, s.releaseView(r, got))
}

func (s *Server) deleteRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	setAudit(r, releaseTarget(rel), "deleted the draft")
	if err := s.Engine.DeleteRelease(rel.ID); err != nil { //nolint:contextcheck // removing files after the request is answered is not bound to it
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) makeCandidate(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	setAudit(r, releaseTarget(rel), "candidate")
	got, err := s.Engine.MakeCandidate(rel.ID, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.releaseView(r, got))
}

// publishRelease creates the annotated tag (or checks the adopted one) and
// then publishes: the tag is made first, because a tag that exists with no
// release is harmless and can be adopted, while a release without its tag is not.
func (s *Server) publishRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Public bool   `json:"public"`
		Tag    string `json:"tag"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, releaseTarget(rel), "publish")
	got, status, msg := s.publishCore(r, rel, in.Public, in.Tag)
	if got == nil {
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, s.releaseView(r, got))
}

// publishCore makes the annotated tag and fixes the release. On a refusal it returns no release, the status and the words.
func (s *Server) publishCore(r *http.Request, rel *engine.Release, public bool, tagOverride string) (*engine.Release, int, string) {
	id := identityFrom(r)
	if rel.State != engine.ReleaseDraft && rel.State != engine.ReleaseCandidate {
		return nil, http.StatusConflict, fmt.Sprintf("release %s %s is already %s", rel.Project, rel.Version, rel.State)
	}
	if public {
		if !s.isProjectAdmin(id, rel.Project) {
			return nil, http.StatusForbidden, "only an administrator of " + rel.Project + " may make a release public"
		}
		set, _ := s.Engine.ReleaseSettingsOf(rel.Project)
		if ok, _ := s.Engine.PublicReleasesAllowed(); !ok || !set.Public {
			return nil, http.StatusConflict, "public downloads are off: the installation must allow them and the {project}'s release settings must ask for them"
		}
	}
	if s.Projects == nil {
		return nil, http.StatusNotFound, "no {projects} are configured"
	}
	set, _ := s.Engine.ReleaseSettingsOf(rel.Project)
	tag := rel.Tag
	if tag == "" {
		tag = nonEmpty(tagOverride, release.TagName(set.TagPrefix, rel.Version))
	}
	pi := engine.PublishInput{Tag: tag, Actor: id.User, Public: public}
	ti, err := s.Projects.ReleaseTag(r.Context(), rel.Project, tag)
	if err != nil {
		return s.refusal(err)
	}
	switch {
	case ti.Exists && ti.Commit != rel.SHA:
		return nil, http.StatusConflict, fmt.Sprintf("the tag %s already points to %s, not the release's commit %s; a tag is never moved. Release under another version", tag, ti.Commit[:8], rel.SHA[:8])
	case ti.Exists:
		pi.TagObject, pi.TagCreated = ti.Object, false
	case !s.canTag(r.Context(), rel.Project):
		return nil, http.StatusConflict, "Launchway cannot write to " + rel.Project + "'s repository (it is not hosted here, and its configuration has no push section), so it cannot create the tag " + tag + ". Create the annotated tag yourself (git tag -a " + tag + " " + rel.SHA[:8] + " && git push origin " + tag + ") and publish again"
	case s.isHosted(r.Context(), rel.Project):
		repo, _ := s.hostedRepoOfFlight(r.Context(), rel.Project)
		obj, err := s.createHostedTag(r, repo, tag, rel.SHA, "Release "+rel.Version+"\n\n"+firstParagraph(rel.Notes))
		if err != nil {
			return s.refusal(err)
		}
		pi.TagObject, pi.TagCreated = obj, true
	default:
		made, err := s.Projects.CreateTag(r.Context(), rel.Project, tag, rel.SHA, "Release "+rel.Version+"\n\n"+firstParagraph(rel.Notes),
			source.Author{User: id.User, Name: id.Profile.Name, Email: id.Profile.Email}, s.Now())
		if err != nil {
			return s.refusal(err)
		}
		pi.TagObject, pi.TagCreated = made.Object, true
	}
	got, err := s.Engine.PublishRelease(rel.ID, pi)
	if err != nil {
		return s.refusal(err)
	}
	s.Engine.NoticeReleasePublished(got) //nolint:contextcheck // a notification outlives the request
	s.noticeRelease(context.WithoutCancel(r.Context()), got.Project, got.Version, identityFrom(r).User)
	s.queueOutside(context.WithoutCancel(r.Context()), got)
	setAudit(r, releaseTarget(got), "published as tag "+got.Tag)
	return got, http.StatusOK, ""
}

// firstParagraph is what the tag says about the release: the notes without their headings, shortened.
func firstParagraph(md string) string {
	var keep []string
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		keep = append(keep, l)
	}
	text := strings.TrimSpace(strings.Join(keep, "\n"))
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	if r := []rune(text); len(r) > 600 {
		text = strings.TrimSpace(string(r[:600])) + "…"
	}
	return text
}

func (s *Server) yankRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, releaseTarget(rel), clip("withdrawn: "+in.Reason, 300))
	got, err := s.Engine.YankRelease(rel.ID, in.Reason, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.releaseView(r, got))
}

// proposeChangelog offers the notes as a changelog entry in a pull request, never pushed to a branch that people protect.
func (s *Server) proposeChangelog(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	if s.Projects == nil || !s.Projects.WritableForReleases(rel.Project) {
		writeErr(w, http.StatusConflict, "Launchway cannot write to "+rel.Project+"'s repository (its configuration has no push section); copy the notes into the changelog yourself")
		return
	}
	set, _ := s.Engine.ReleaseSettingsOf(rel.Project)
	id := identityFrom(r)
	var in struct {
		Base string `json:"base"`
	}
	if !decode(w, r, &in) {
		return
	}
	base := nonEmpty(in.Base, nonEmpty(rel.Branch, ""))
	setAudit(r, releaseTarget(rel), "changelog "+set.Changelog)
	prop, err := s.Projects.ProposeChangelog(r.Context(), rel.Project, base, set.Changelog, rel.Version, rel.Notes,
		source.Author{User: id.User, Name: id.Profile.Name, Email: id.Profile.Email}, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"proposal": prop}
	// A hosted repository gets the pull request itself.
	if s.Forge != nil && s.Forge.Pulls != nil {
		if repo, err := s.Projects.ForgeRepository(r.Context(), rel.Project); err == nil {
			if p, err := s.Forge.Pulls.Open(r.Context(), repo.ID, id.User, pulls.New{Title: "Changelog for " + rel.Version, Body: "Adds the notes of release " + rel.Version + " to " + set.Changelog + ".",
				SourceBranch: prop.Branch, TargetBranch: prop.Base}); err == nil {
				out["pull_request"] = map[string]any{"number": p.Number, "url": s.baseURL(r) + "/repositories/" + repo.Path + "/pulls/" + strconv.Itoa(p.Number)}
			} else {
				out["pull_request_error"] = err.Error()
			}
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- assets ----

func (s *Server) uploadReleaseAsset(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	name := r.PathValue("asset")
	setAudit(r, releaseTarget(rel), "file "+name)
	body := http.MaxBytesReader(w, r.Body, engine.MaxAssetBytes+1)
	a, err := s.Engine.AddReleaseFile(r.Context(), rel.ID, name, strings.ToLower(r.Header.Get("X-Launchway-SHA256")), body, engine.Provenance{}, identityFrom(r).User)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "a release file is limited to 2 GiB")
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// addReleaseAssets attaches Cargo files of a Sortie and the Sortie's image digests.
func (s *Server) addReleaseAssets(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		RunID string `json:"run_id"`
		// Files names Cargo files to copy: {artifact, path, name}. None, with run_id: only the Sortie's images, SBOM and scans are recorded.
		Files []struct {
			Artifact string `json:"artifact"`
			Path     string `json:"path"`
			Name     string `json:"name"`
		} `json:"files"`
		Images bool `json:"images"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.RunID == "" {
		writeErr(w, http.StatusBadRequest, "say which Sortie the files come from: {\"run_id\": \"...\", \"files\": [{\"artifact\": \"...\", \"path\": \"...\"}], \"images\": true}")
		return
	}
	if p, err := s.Engine.RunProject(in.RunID); err != nil || p != rel.Project {
		writeErr(w, http.StatusNotFound, "no such run in "+rel.Project)
		return
	}
	setAudit(r, releaseTarget(rel), fmt.Sprintf("from run %s: %d files, images %v", in.RunID, len(in.Files), in.Images))
	id := identityFrom(r)
	added := []engine.ReleaseAsset{}
	for _, f := range in.Files {
		a, err := s.Engine.AddReleaseCargo(r.Context(), rel.ID, in.RunID, f.Artifact, f.Path, f.Name, id.User)
		if err != nil {
			s.fail(w, err)
			return
		}
		added = append(added, *a)
	}
	if in.Images || len(in.Files) == 0 {
		more, err := s.Engine.AddReleaseRunOutputs(rel.ID, in.RunID, id.User)
		if err != nil {
			s.fail(w, err)
			return
		}
		added = append(added, more...)
	}
	writeJSON(w, http.StatusCreated, added)
}

func (s *Server) removeReleaseAsset(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	setAudit(r, releaseTarget(rel), "removed "+r.PathValue("asset"))
	if err := s.Engine.RemoveReleaseAsset(r.Context(), rel.ID, r.PathValue("asset")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// downloadReleaseAsset serves a file to a signed-in caller who may read the project.
func (s *Server) downloadReleaseAsset(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	s.serveAsset(w, r, rel)
}

// publicDownload serves a file with no sign-in, only when the installation
// allows public downloads, the project asks for them, and the release is
// published and was made public. Everything else is "not found".
func (s *Server) publicDownload(w http.ResponseWriter, r *http.Request) {
	notFound := func() { writeErr(w, http.StatusNotFound, "not found") }
	ok, err := s.Engine.PublicReleasesAllowed()
	if err != nil || !ok {
		notFound()
		return
	}
	project := r.PathValue("name")
	set, err := s.Engine.ReleaseSettingsOf(project)
	if err != nil || !set.Public {
		notFound()
		return
	}
	rel, ok2 := s.publicRelease(project, r.PathValue("version"))
	if !ok2 {
		notFound()
		return
	}
	s.serveAsset(w, r, rel)
}

// ---- time ----

func parseTime(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 1e11 {
		return time.UnixMilli(ms), nil
	}
	return def, fmt.Errorf("%w: %q is not a time: use 2026-11-21T09:00:00Z or 2026-11-21", engine.ErrInvalid, s)
}

var _ = sort.Strings
