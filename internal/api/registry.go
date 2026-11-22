package api

// The registry's management API: what a project has pushed, inspecting and
// deleting it, the policies (quota, retention, immutable tags), previews of
// retention and garbage collection. The pushing and pulling itself is /v2/
// (registry_wire.go). The routes are the project's: a viewer sees, an
// operator sees, an administrator of the project deletes and sets policy. The
// installation-wide ones (status, collection, installation policy) are for
// signed-in people to read and administrators to change.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/registry"
)

func (s *Server) registryRoutes() []route {
	return []route{
		{"GET /api/v1/registry", signedIn, "", s.registryStatus},
		{"GET /api/v1/registry/gc", administer, "", s.registryLastGC},
		{"POST /api/v1/registry/gc", administer, "registry.gc_run", s.registryRunGC},
		{"GET /api/v1/registry/policy", administer, "", s.registryGetPolicy},
		{"PUT /api/v1/registry/policy", administer, "registry.policy", s.registrySetPolicy},
		{"GET /api/v1/projects/{name}/registry", projectRead, "", s.projectRegistry},
		{"GET /api/v1/projects/{name}/registry/images", projectRead, "", s.projectImages},
		{"GET /api/v1/projects/{name}/registry/image", projectRead, "", s.projectImage},
		{"DELETE /api/v1/projects/{name}/registry/image", projectOperate, "registry.image_delete", s.deleteProjectImage},
		{"DELETE /api/v1/projects/{name}/registry/tag", projectOperate, "registry.tag_delete", s.deleteProjectTag},
		{"GET /api/v1/projects/{name}/registry/policy", projectRead, "", s.projectRegistryPolicy},
		{"PUT /api/v1/projects/{name}/registry/policy", projectOperate, "registry.policy", s.setProjectRegistryPolicy},
		{"GET /api/v1/projects/{name}/registry/retention", projectRead, "", s.retentionPreview},
		{"POST /api/v1/projects/{name}/registry/retention", projectOperate, "registry.retention_run", s.retentionRun},
	}
}

// registryOn answers 404 feature_off when the registry is off.
func (s *Server) registryOn(w http.ResponseWriter) bool {
	if s.Registry == nil || s.Registry.Svc == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": registryOff, "code": "feature_off",
			"next": "Set registry.enabled to true (the default) on a server with sign-in and a blob store, then restart."})
		return false
	}
	return true
}

func (s *Server) registryFail(w http.ResponseWriter, err error) {
	var re *registry.Error
	if errors.As(err, &re) {
		body := map[string]any{"error": re.Message, "code": strings.ToLower(re.Code)}
		if re.Next != "" {
			body["next"] = re.Next
		}
		writeJSON(w, re.Status, body)
		return
	}
	s.fail(w, err)
}

// repositoryParam reads the repository a request names and checks it is in
// the project of the route.
func (s *Server) repositoryParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	repo := r.URL.Query().Get("repository")
	project := r.PathValue("name")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "say which repository: ?repository="+project+"/image")
		return "", false
	}
	if registry.ProjectOf(repo) != project {
		writeErr(w, http.StatusNotFound, "no such repository in this {project}")
		return "", false
	}
	return repo, true
}

// mayAdminProject reports whether the caller is an administrator of the
// project of the route; the route class lets operators through, the handler
// is stricter for what is hard to undo.
func (s *Server) mayAdminProject(w http.ResponseWriter, r *http.Request) bool {
	id, err := s.requestIdentity(r)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !s.authorize(id, auth.AdministerProject, r.PathValue("name")) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you need the admin role on " + r.PathValue("name") + " to do this",
			"next": "Ask a {project} administrator, or have one change the policy."})
		return false
	}
	return true
}

func (s *Server) registryActor(r *http.Request) registry.Who {
	id, _ := s.requestIdentity(r)
	if id == nil {
		return registry.Who{Name: s.Actor}
	}
	return registry.Who{Name: id.User}
}

func pullCommand(host, repo string, im registry.Manifest) string {
	ref := "@" + im.Digest
	if len(im.Tags) > 0 {
		ref = ":" + im.Tags[0]
	}
	return host + "/" + repo + ref
}

type registryStatusOut struct {
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
	// Host is what clients write before a repository name.
	Host     string `json:"host"`
	Insecure bool   `json:"insecure"`
	Store    string `json:"store"`
	// Encryption says plainly what protects blobs at rest in this build.
	Encryption   registryEncryption `json:"encryption"`
	Limits       registryLimitsOut  `json:"limits"`
	Repositories int                `json:"repositories"`
	LastGC       *registry.GCReport `json:"last_gc,omitempty"`
	LastGCAt     int64              `json:"last_gc_at,omitempty"`
	Login        string             `json:"login"`
}

type registryEncryption struct {
	BlobsEncryptedByLaunchway bool   `json:"blobs_encrypted_by_launchway"`
	Note                      string `json:"note"`
}

type registryLimitsOut struct {
	MaxBlobBytes      int64 `json:"max_blob_bytes"`
	MaxManifestBytes  int64 `json:"max_manifest_bytes"`
	DefaultQuotaBytes int64 `json:"default_quota_bytes"`
	RequestsPerMinute int   `json:"requests_per_minute"`
	GCGraceSeconds    int64 `json:"gc_grace_seconds"`
}

// RegistryEncryptionNote is said wherever the registry's storage is described.
const RegistryEncryptionNote = "Launchway does not encrypt the registry's blobs itself in this release: layers, configs and manifests are stored as they are pushed, like artifacts. " +
	"Encrypt the volume or database that holds the blob store (and the S3 bucket, with server-side encryption). Per-object encryption under the installation's keys is the next slice."

func (s *Server) registryStatus(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	h := s.Registry
	host, insecure := s.registryHost(r)
	l := h.Svc.Limits()
	repos, err := h.Svc.ListRepositories(r.Context(), "", nil)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	out := registryStatusOut{Enabled: true, Address: s.registryBase(r), Host: host, Insecure: insecure, Store: h.StoreName,
		Encryption: registryEncryption{false, RegistryEncryptionNote},
		Limits:     registryLimitsOut{l.MaxBlobBytes, l.MaxManifestBytes, l.DefaultQuotaBytes, l.RequestsPerMinute, int64(l.GCGrace.Seconds())},
		Login:      "docker login " + host + "   (user: anything; password: a Launchway API token)"}
	// Only the repositories the caller may see are counted.
	if id, err := s.requestIdentity(r); err == nil {
		for _, repo := range repos {
			if s.authorize(id, auth.Read, repo.Project) {
				out.Repositories++
			}
		}
	}
	if id, _ := s.requestIdentity(r); id != nil && s.authorize(id, auth.Administer, "") {
		if rep, at, ok, err := h.Svc.LastGC(r.Context()); err == nil && ok {
			out.LastGC, out.LastGCAt = &rep, at
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) registryLastGC(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	rep, at, ok, err := s.Registry.Svc.LastGC(r.Context())
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ran": ok, "at": at, "report": rep})
}

func (s *Server) registryRunGC(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	var in struct {
		DryRun bool `json:"dry_run"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "", map[bool]string{true: "dry run", false: "collection"}[in.DryRun])
	rep, err := s.Registry.Svc.GC(r.Context(), registry.GCOptions{DryRun: in.DryRun})
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type policyRef struct {
	Scope string `json:"scope"`
	Name  string `json:"name,omitempty"`
}

func (s *Server) registryGetPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	scope, name := r.URL.Query().Get("scope"), r.URL.Query().Get("name")
	if scope == "" {
		scope = registry.ScopeInstallation
	}
	if scope != registry.ScopeInstallation && scope != registry.ScopeOrganization {
		writeErr(w, http.StatusBadRequest, "this route sets the installation's or an organization's policy; a {project}'s is under /api/v1/projects/{name}/registry/policy")
		return
	}
	p, set, err := s.Registry.Svc.GetPolicy(r.Context(), scope, name)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "name": name, "set": set, "policy": p})
}

func (s *Server) registrySetPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	var in struct {
		policyRef
		Policy registry.Policy `json:"policy"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Scope == "" {
		in.Scope = registry.ScopeInstallation
	}
	if in.Scope != registry.ScopeInstallation && in.Scope != registry.ScopeOrganization {
		writeErr(w, http.StatusBadRequest, "scope is installation or organization here; a {project}'s policy is under /api/v1/projects/{name}/registry/policy")
		return
	}
	setAudit(r, in.Scope+" "+in.Name, "")
	if err := s.Registry.Svc.SetPolicy(r.Context(), in.Scope, in.Name, in.Policy, s.registryActor(r).Name); err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": in.Scope, "name": in.Name, "policy": in.Policy})
}

type registryRepoOut struct {
	registry.Repository
	Images int   `json:"images"`
	Tags   int   `json:"tags"`
	Bytes  int64 `json:"bytes"`
}

// projectRegistry is the project's registry at a glance: its address, its
// repositories, its usage against the quota, the policy in force.
func (s *Server) projectRegistry(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	project := r.PathValue("name")
	svc := s.Registry.Svc
	host, insecure := s.registryHost(r)
	repos, err := svc.ListRepositories(r.Context(), project, nil)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	outRepos := []registryRepoOut{}
	for _, repo := range repos {
		o := registryRepoOut{Repository: repo}
		imgs, err := svc.Images(r.Context(), repo.Name)
		if err != nil {
			s.registryFail(w, err)
			return
		}
		o.Images = len(imgs)
		for _, im := range imgs {
			o.Tags += len(im.Tags)
			o.Bytes += im.TotalSize
		}
		outRepos = append(outRepos, o)
	}
	used, err := svc.ProjectUsage(r.Context(), project)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	eff, err := svc.EffectivePolicy(r.Context(), project)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "host": host, "insecure": insecure, "repositories": outRepos,
		"used_bytes": used, "quota_bytes": eff.QuotaBytes, "quota_from": eff.QuotaFrom, "policy": eff,
		"push_example": host + "/" + project + "/<image>:<tag>", "encryption": registryEncryption{false, RegistryEncryptionNote}})
}

type projectImageOut struct {
	registry.Image
	Pull string `json:"pull"`
}

func (s *Server) projectImages(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	repo, ok := s.repositoryParam(w, r)
	if !ok {
		return
	}
	imgs, err := s.Registry.Svc.Images(r.Context(), repo)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	host, _ := s.registryHost(r)
	out := []projectImageOut{}
	for _, im := range imgs {
		out = append(out, projectImageOut{Image: im, Pull: pullCommand(host, repo, im.Manifest)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo, "images": out})
}

func (s *Server) projectImage(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	repo, ok := s.repositoryParam(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("reference")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "say which image: ?reference=<tag or digest>")
		return
	}
	im, err := s.Registry.Svc.InspectImage(r.Context(), repo, ref)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	_, body, err := s.Registry.Svc.ResolveManifest(r.Context(), repo, im.Digest, true)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	host, _ := s.registryHost(r)
	writeJSON(w, http.StatusOK, map[string]any{"image": projectImageOut{Image: im, Pull: pullCommand(host, repo, im.Manifest)}, "manifest": string(body)})
}

func (s *Server) deleteProjectImage(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) || !s.mayAdminProject(w, r) {
		return
	}
	repo, ok := s.repositoryParam(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("reference")
	setAudit(r, repo+"@"+ref, "")
	im, err := s.Registry.Svc.InspectImage(r.Context(), repo, ref)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	gone, err := s.Registry.Svc.DeleteManifest(r.Context(), repo, im.Digest, s.registryActor(r))
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo, "digest": im.Digest, "deleted": gone,
		"note": "the bytes are removed by garbage collection once nothing refers to them"})
}

func (s *Server) deleteProjectTag(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) || !s.mayAdminProject(w, r) {
		return
	}
	repo, ok := s.repositoryParam(w, r)
	if !ok {
		return
	}
	tag := r.URL.Query().Get("tag")
	setAudit(r, repo+":"+tag, "")
	if err := s.Registry.Svc.DeleteTag(r.Context(), repo, tag, s.registryActor(r)); err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo, "tag": tag, "deleted": true})
}

func (s *Server) projectRegistryPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	project := r.PathValue("name")
	scope, name := registry.ScopeProject, project
	if repo := r.URL.Query().Get("repository"); repo != "" {
		if registry.ProjectOf(repo) != project {
			writeErr(w, http.StatusNotFound, "no such repository in this {project}")
			return
		}
		scope, name = registry.ScopeRepository, repo
	}
	p, set, err := s.Registry.Svc.GetPolicy(r.Context(), scope, name)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	eff, err := s.Registry.Svc.EffectivePolicy(r.Context(), name)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "name": name, "set": set, "policy": p, "effective": eff})
}

func (s *Server) setProjectRegistryPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) || !s.mayAdminProject(w, r) {
		return
	}
	project := r.PathValue("name")
	var in registry.Policy
	if !decode(w, r, &in) {
		return
	}
	scope, name := registry.ScopeProject, project
	if repo := r.URL.Query().Get("repository"); repo != "" {
		if registry.ProjectOf(repo) != project {
			writeErr(w, http.StatusNotFound, "no such repository in this {project}")
			return
		}
		scope, name = registry.ScopeRepository, repo
	}
	// A quota and the organization-wide limits are the installation's to raise: a
	// project may lower its own quota, not lift it above the default.
	if in.QuotaBytes != nil && scope == registry.ScopeProject {
		id, _ := s.requestIdentity(r)
		if id != nil && !s.authorize(id, auth.Administer, "") {
			cur, _ := s.Registry.Svc.EffectivePolicy(r.Context(), project)
			if *in.QuotaBytes == 0 || (cur.QuotaBytes >= 0 && *in.QuotaBytes > cur.QuotaBytes) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an administrator of the installation may raise a {project}'s quota",
					"next": "Lower the quota here, or ask an installation administrator to raise it (PUT /api/v1/registry/policy with a {project} policy)."})
				return
			}
		}
	}
	setAudit(r, scope+" "+name, "")
	if err := s.Registry.Svc.SetPolicy(r.Context(), scope, name, in, s.registryActor(r).Name); err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "name": name, "policy": in})
}

func (s *Server) retentionPreview(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) {
		return
	}
	scope, name := s.retentionScope(r)
	cands, err := s.Registry.Svc.RetentionPreview(r.Context(), scope, name)
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": true, "candidates": nonNilList(cands)})
}

func (s *Server) retentionRun(w http.ResponseWriter, r *http.Request) {
	if !s.registryOn(w) || !s.mayAdminProject(w, r) {
		return
	}
	scope, name := s.retentionScope(r)
	setAudit(r, scope+" "+name, "")
	done, err := s.Registry.Svc.RunRetention(r.Context(), scope, name, s.registryActor(r))
	if err != nil {
		s.registryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": false, "deleted": nonNilList(done)})
}

func (s *Server) retentionScope(r *http.Request) (scope, name string) {
	if repo := r.URL.Query().Get("repository"); repo != "" && registry.ProjectOf(repo) == r.PathValue("name") {
		return registry.ScopeRepository, repo
	}
	return registry.ScopeProject, r.PathValue("name")
}

func nonNilList[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
