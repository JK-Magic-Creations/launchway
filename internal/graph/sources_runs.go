package graph

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// SortieSource reads the results of a project's Sorties: the images that
// were built (their digest, the platforms of the index), the commit each was
// built from, and the components of its SBOM when the step made one. It is
// what was built, as the pipeline file's image steps are what was planned.
type SortieSource struct {
	DB  *engine.Database
	Cfg Config
	// Window is how many recent image results are asserted per project (50).
	Window int
	// MaxComponents bounds the SBOM components kept per built thing (2000).
	MaxComponents int
}

// Name is "sorties".
func (SortieSource) Name() string { return "sorties" }

// Scopes is projects.
func (SortieSource) Scopes() []string { return []string{ScopeProject} }

// BuiltKey is the key of a built thing: the image name and the digest.
func BuiltKey(stream, digest string) string { return stream + "@" + digest }

// CommitKey is the key of a commit node.
func CommitKey(repo, sha string) string { return repo + "@" + sha }

// repoOfProject maps a project to the hosted repository it builds.
func (c Config) repoOfProject(ctx context.Context, project string) string {
	if c.ProjectRepos == nil {
		return ""
	}
	m, err := c.ProjectRepos(ctx)
	if err != nil {
		return ""
	}
	return m[project]
}

// Assert reads the project's recent image results.
func (s SortieSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	window, maxc := s.Window, s.MaxComponents
	if window <= 0 {
		window = 50
	}
	if maxc <= 0 {
		maxc = 2000
	}
	repo := s.Cfg.repoOfProject(ctx, sc.Ref)
	sub := ProjectSubject(sc.Ref)
	pk := NodeRef{KindPipeline, PipelineKey(firstNonEmpty(repo, "project/"+sc.Ref))}
	a.Add(Node{Kind: KindPipeline, Key: pk.Key, Name: pk.Key, Subject: sub})
	rows, err := s.DB.Query(ctx, `SELECT i.run_id, i.step_id, i.attempt, i.run_number, i.digest, i.created_at, r.sha FROM images i JOIN runs r ON r.id = i.run_id
		WHERE i.project = ? AND i.digest <> '' AND i.pushed = 1 ORDER BY i.created_at DESC, i.run_id LIMIT ?`, sc.Ref, window)
	if err != nil {
		return a, "", err
	}
	type res struct {
		run, step    string
		attempt, num int
		digest, sha  string
		at           int64
	}
	var rs []res
	for rows.Next() {
		var r res
		if err := rows.Scan(&r.run, &r.step, &r.attempt, &r.num, &r.digest, &r.at, &r.sha); err != nil {
			rows.Close()
			return a, "", err
		}
		rs = append(rs, r)
	}
	if err := rows.Close(); err != nil {
		return a, "", err
	}
	last := ""
	for _, r := range rs {
		if last == "" {
			last = fmt.Sprintf("run %d", r.num)
		}
		refs, err := s.DB.Query(ctx, `SELECT kind, repository, digest, platform FROM image_refs WHERE run_id = ? AND step_id = ? AND attempt = ? AND kind IN ('index','platform')`, r.run, r.step, r.attempt)
		if err != nil {
			return a, "", err
		}
		stream, indexKey := "", ""
		type plat struct{ repo, digest, platform string }
		var plats []plat
		for refs.Next() {
			var kind, rp, dg, pl string
			if err := refs.Scan(&kind, &rp, &dg, &pl); err != nil {
				refs.Close()
				return a, "", err
			}
			if kind == "index" {
				stream = NormalizeImage(rp)
			} else {
				plats = append(plats, plat{rp, dg, pl})
			}
		}
		if err := refs.Close(); err != nil {
			return a, "", err
		}
		if stream == "" || stream == "docker.io/library/" {
			continue
		}
		a.Add(Node{Kind: KindImage, Key: stream, Name: stream, Attrs: map[string]any{"role": "built"}})
		indexKey = BuiltKey(stream, r.digest)
		attrs := map[string]any{"digest": r.digest, "run": r.num, "step": r.step, "project": sc.Ref, "at": r.at}
		a.Add(Node{Kind: KindBuilt, Key: indexKey, Name: shortDigestName(stream, r.digest), Subject: sub, Attrs: attrs})
		a.Link(Edge{Kind: EdgeBuilds, From: pk, To: NodeRef{KindBuilt, indexKey}, Qualifier: r.step, Attrs: map[string]any{"run": r.num, "at": r.at}, Subject: sub, Ref: r.sha})
		a.Link(Edge{Kind: EdgeInstanceOf, From: NodeRef{KindBuilt, indexKey}, To: NodeRef{KindImage, stream}, Subject: sub, Ref: r.sha})
		if r.sha != "" {
			ck := CommitKey(firstNonEmpty(repo, "project/"+sc.Ref), r.sha)
			a.Add(Node{Kind: KindCommit, Key: ck, Name: shortRef(r.sha), Subject: sub, Attrs: map[string]any{"sha": r.sha, "repository": repo}})
			a.Link(Edge{Kind: EdgeBuiltFrom, From: NodeRef{KindBuilt, indexKey}, To: NodeRef{KindCommit, ck}, Subject: sub, Ref: r.sha})
		}
		for _, p := range plats {
			pkey := BuiltKey(stream, p.digest)
			a.Add(Node{Kind: KindBuilt, Key: pkey, Name: shortDigestName(stream, p.digest) + " " + p.platform, Subject: sub,
				Attrs: map[string]any{"digest": p.digest, "platform": p.platform, "arch": Arch(p.platform), "index": r.digest, "project": sc.Ref, "at": r.at}})
			a.Link(Edge{Kind: EdgeInstanceOf, From: NodeRef{KindBuilt, pkey}, To: NodeRef{KindImage, stream}, Qualifier: p.platform, Subject: sub, Ref: r.sha})
			a.Link(Edge{Kind: EdgeBuiltFrom, From: NodeRef{KindBuilt, pkey}, To: NodeRef{KindBuilt, indexKey}, Qualifier: "platform", Subject: sub, Ref: r.sha})
		}
		// SBOM components: what the image includes.
		comps, err := s.DB.Query(ctx, `SELECT name, version, type, purl FROM sbom_components WHERE run_id = ? AND step_id = ? AND attempt = ? ORDER BY id LIMIT ?`, r.run, r.step, r.attempt, maxc)
		if err != nil {
			return a, "", err
		}
		for comps.Next() {
			var name, version, typ, purl string
			if err := comps.Scan(&name, &version, &typ, &purl); err != nil {
				comps.Close()
				return a, "", err
			}
			key := PackageKeyFromPurl(purl, name)
			a.Add(Node{Kind: KindPackage, Key: key, Name: name})
			a.Link(Edge{Kind: EdgeContainsPkg, From: NodeRef{KindBuilt, indexKey}, To: NodeRef{KindPackage, key}, Resolved: version, Attrs: map[string]any{"type": typ}, Subject: sub, Ref: r.sha})
		}
		if err := comps.Close(); err != nil {
			return a, "", err
		}
	}
	return a, last, nil
}

func shortDigestName(stream, digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return stream + "@" + d
}

// PackageKeyFromPurl maps a package URL to the key packages are known by:
// go, npm and pypi packages meet the manifests' packages; anything else (an
// operating-system package) is kept under its own purl type.
func PackageKeyFromPurl(purl, fallback string) string {
	if !strings.HasPrefix(purl, "pkg:") {
		return "other:" + fallback
	}
	rest := strings.TrimPrefix(purl, "pkg:")
	rest, _, _ = strings.Cut(rest, "?")
	rest, _, _ = strings.Cut(rest, "#")
	typ, body, ok := strings.Cut(rest, "/")
	if !ok {
		return "other:" + fallback
	}
	if i := strings.LastIndex(body, "@"); i > 0 {
		body = body[:i]
	}
	if u, err := url.PathUnescape(body); err == nil {
		body = u
	}
	switch typ {
	case "golang":
		return "go:" + body
	case "npm":
		return "npm:" + body
	case "pypi":
		return PackageKey("python", body)
	}
	return typ + ":" + body
}

// DeploymentSource reads what is deployed now: for every Airfield, project
// and service, the deployment in force, the digests its run built, and when
// it was delivered.
type DeploymentSource struct {
	DB  *engine.Database
	Cfg Config
}

// Name is "deployments".
func (DeploymentSource) Name() string { return "deployments" }

// Scopes is the installation.
func (DeploymentSource) Scopes() []string { return []string{ScopeInstallation} }

// DeploymentKey is the key of a deployment node.
func DeploymentKey(env, project, service string) string { return env + "/" + project + "/" + service }

// Assert reads every current deployment.
func (d DeploymentSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	rows, err := d.DB.Query(ctx, `SELECT s.environment, s.project, s.service, s.since, d.id, d.run_id, d.run_number, d.version, d.commit_sha, d.application, d.deployed_at, d.repo
		FROM environment_state s JOIN deployments d ON d.id = s.current_deployment WHERE s.current_deployment <> '' ORDER BY s.environment, s.project, s.service`)
	if err != nil {
		return a, "", err
	}
	type dep struct {
		env, project, service, id, run, version, sha, app, repo string
		since, deployedAt                                       int64
		num                                                     int
	}
	var ds []dep
	for rows.Next() {
		var x dep
		if err := rows.Scan(&x.env, &x.project, &x.service, &x.since, &x.id, &x.run, &x.num, &x.version, &x.sha, &x.app, &x.deployedAt, &x.repo); err != nil {
			rows.Close()
			return a, "", err
		}
		ds = append(ds, x)
	}
	if err := rows.Close(); err != nil {
		return a, "", err
	}
	for _, x := range ds {
		sub := ProjectSubject(x.project)
		af := NodeRef{KindAirfield, x.env}
		a.Add(Node{Kind: KindAirfield, Key: x.env, Name: x.env})
		key := DeploymentKey(x.env, x.project, x.service)
		dn := NodeRef{KindDeployment, key}
		at := x.deployedAt
		if at == 0 {
			at = x.since
		}
		a.Add(Node{Kind: KindDeployment, Key: key, Name: key, Subject: sub, Attrs: map[string]any{"airfield": x.env, "project": x.project, "service": x.service,
			"version": x.version, "commit": x.sha, "run": x.num, "application": x.app, "since": x.since}})
		a.Link(Edge{Kind: EdgeRunsOn, From: dn, To: af, Subject: sub})
		imgs, err := d.DB.Query(ctx, `SELECT r.repository, r.digest FROM image_refs r WHERE r.run_id = ? AND r.kind = 'index' AND r.pushed = 1 ORDER BY r.step_id LIMIT 20`, x.run)
		if err != nil {
			return a, "", err
		}
		for imgs.Next() {
			var rp, dg string
			if err := imgs.Scan(&rp, &dg); err != nil {
				imgs.Close()
				return a, "", err
			}
			stream := NormalizeImage(rp)
			bk := BuiltKey(stream, dg)
			a.Link(Edge{Kind: EdgeDeliveredTo, From: NodeRef{KindBuilt, bk}, To: dn, Qualifier: dg, Attrs: map[string]any{"digest": dg, "at": at, "airfield": x.env, "version": x.version}, Subject: sub, Ref: x.sha})
		}
		if err := imgs.Close(); err != nil {
			return a, "", err
		}
	}
	return a, fmt.Sprintf("%d deployments", len(ds)), nil
}
