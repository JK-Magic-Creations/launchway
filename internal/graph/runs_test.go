package graph

import (
	"context"
	"strings"
	"testing"
)

type fakeInv struct {
	ms []MachineInfo
	ps []PlatformInfo
}

func (f fakeInv) Machines(context.Context) ([]MachineInfo, error)   { return f.ms, nil }
func (f fakeInv) Platforms(context.Context) ([]PlatformInfo, error) { return f.ps, nil }

// seedRun writes what the engine keeps of a Sortie that built an image and a deployment that delivered it.
func seedRun(t *testing.T, s *Store, project, run string, num int, sha, repo, digest, archDigest string) {
	t.Helper()
	ex := func(q string, a ...any) {
		t.Helper()
		if _, err := s.DB.Exec(bg, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ex(`INSERT INTO runs(id, project, number, pipeline, status, sha, created_at, last_seq) VALUES(?,?,?,?,?,?,?,?)`, run, project, num, "p", "succeeded", sha, 1000, 1)
	ex(`INSERT INTO images(run_id, step_id, attempt, project, run_number, digest, image, pushed, summary, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, run, "image", 1, project, num, digest, repo+"@"+digest, 1, "{}", 2000+int64(num))
	ex(`INSERT INTO image_refs(run_id, step_id, attempt, project, run_number, kind, repository, tag, digest, platform, pushed, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, run, "image", 1, project, num, "index", repo, "", digest, "", 1, 2000)
	ex(`INSERT INTO image_refs(run_id, step_id, attempt, project, run_number, kind, repository, tag, digest, platform, pushed, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, run, "image", 1, project, num, "platform", repo, "", archDigest, "linux/arm64", 1, 2000)
	ex(`INSERT INTO sbom_components(run_id, step_id, attempt, project, run_number, name, name_key, version, type, purl) VALUES(?,?,?,?,?,?,?,?,?,?)`, run, "image", 1, project, num, "libgo", "libgo", "v1.2.0", "library", "pkg:golang/example.test/libgo@v1.2.0")
	ex(`INSERT INTO sbom_components(run_id, step_id, attempt, project, run_number, name, name_key, version, type, purl) VALUES(?,?,?,?,?,?,?,?,?,?)`, run, "image", 1, project, num, "openssl", "openssl", "3.0.1", "library", "pkg:deb/debian/openssl@3.0.1")
}

func seedDeployment(t *testing.T, s *Store, env, project, service, run string, num int, version, sha string) {
	t.Helper()
	id := "dep_" + env + project + service
	if _, err := s.DB.Exec(bg, `INSERT INTO deployments(id, run_id, run_number, project, step_id, environment, service, target, application, mode, version, commit_sha, opened_at, deployed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, run, num, project, "deploy", env, service, "argocd", "app-"+service, "gitops", version, sha, 3000, 3100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(bg, `INSERT INTO environment_state(environment, project, service, current_deployment, current_version, current_run, current_run_number, since) VALUES(?,?,?,?,?,?,?,?)`,
		env, project, service, id, version, run, num, 3100); err != nil {
		t.Fatal(err)
	}
}

func TestSortiesAndDeploymentsAnswerWhereItRunsAndWhatIsInIt(t *testing.T) {
	repos := fixtureRepos()
	r := newRunner(t, repos)
	cfg := Config{RegistryHosts: map[string]string{"harbor": "harbor.test"}, ProjectRepos: func(context.Context) (map[string]string, error) { return map[string]string{"svc": "acme/svc"}, nil }}
	r.Sources = append(r.Sources, SortieSource{DB: r.Store.DB, Cfg: cfg}, DeploymentSource{DB: r.Store.DB, Cfg: cfg})
	r.Projects = func(context.Context) ([]string, error) { return []string{"svc"}, nil }
	seedRun(t, r.Store, "svc", "run1", 1, "c-svc-1", "harbor.test/apps/svc", "sha256:aaa111aaa111aaa111", "sha256:bbb222bbb222")
	seedDeployment(t, r.Store, "prod", "svc", "api", "run1", 1, "1.0.0", "c-svc-1")
	seedDeployment(t, r.Store, "dev", "svc", "api", "run1", 1, "1.0.0", "c-svc-1")
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	// Where is libgo running? Through the SBOM (exact) and through the dependency.
	w, err := r.Store.Where(bg, AllowAll{}, ref(KindPackage, "go:example.test/libgo"), Limits{}, When{})
	if err != nil || len(w.Deployments) != 2 || strings.Join(w.Airfields, ",") != "dev,prod" {
		t.Fatalf("%+v %v", w, err)
	}
	d := w.Deployments[0]
	if d.Digest != "sha256:aaa111aaa111aaa111" || d.Version != "1.0.0" || !d.Exact || len(d.Why) < 2 {
		t.Fatalf("%+v", d)
	}
	// A commit.
	w, _ = r.Store.Where(bg, AllowAll{}, ref(KindCommit, "acme/svc@c-svc-1"), Limits{}, When{})
	if len(w.Deployments) != 2 || !w.Deployments[0].Exact {
		t.Fatalf("%+v", w)
	}
	// What is in the production deployment.
	c, err := r.Store.Contents(bg, AllowAll{}, ref(KindDeployment, "prod/svc/api"), Limits{}, When{})
	if err != nil {
		t.Fatal(err)
	}
	var pk []string
	for _, p := range c.Packages {
		pk = append(pk, p.Node.Key)
	}
	if !contains(pk, "go:example.test/libgo") || !contains(pk, "other:openssl") && !contains(pk, "deb:debian/openssl") {
		t.Fatalf("packages %v", pk)
	}
	if len(c.Commits) != 1 || c.Commits[0].Node.Attrs["sha"] != "c-svc-1" || len(c.Images) < 2 {
		t.Fatalf("%+v", c)
	}
	// Impact of the base image reaches the deployments of what is built on it.
	im, _ := r.Store.Impact(bg, AllowAll{}, ref(KindImage, "harbor.test/base/runtime"), Limits{}, When{})
	if len(im.Deployments) != 2 {
		t.Fatalf("%d deployments %+v", len(im.Deployments), im.ByKind)
	}
	// A second run replaces what is current; the old delivery edge closes.
	seedRun(t, r.Store, "svc", "run2", 2, "c-svc-2", "harbor.test/apps/svc", "sha256:ccc333ccc333ccc333", "sha256:ddd444ddd444")
	seedDeployment(t, r.Store, "prod2", "svc", "api", "run2", 2, "1.1.0", "c-svc-2")
	if _, err := r.Update(bg, Scope{ScopeInstallation, ""}); err != nil {
		t.Fatal(err)
	}
	if w, _ = r.Store.Where(bg, AllowAll{}, ref(KindCommit, "acme/svc@c-svc-2"), Limits{}, When{}); len(w.Deployments) != 0 {
		// run2's sortie is a project scope: not asserted until the project is read.
		t.Fatalf("%+v", w)
	}
	if _, err := r.Update(bg, Scope{ScopeProject, "svc"}); err != nil {
		t.Fatal(err)
	}
	if w, _ = r.Store.Where(bg, AllowAll{}, ref(KindCommit, "acme/svc@c-svc-2"), Limits{}, When{}); len(w.Deployments) != 1 || w.Deployments[0].Airfield != "prod2" {
		t.Fatalf("%+v", w)
	}
	// Nobody who cannot read the project sees its deployments.
	none := Only{"repo:acme/svc": true}
	w, _ = r.Store.Where(bg, none, ref(KindPackage, "go:example.test/libgo"), Limits{}, When{})
	if len(w.Deployments) != 0 {
		t.Fatalf("deployments leaked: %+v", w.Deployments)
	}
	// Rebuild equals incremental here too.
	inc, _ := r.Store.Fingerprint(bg)
	if _, err := r.Rebuild(bg); err != nil {
		t.Fatal(err)
	}
	if full, _ := r.Store.Fingerprint(bg); full != inc {
		t.Fatalf("differ:\n%s\n--\n%s", inc, full)
	}
}

func TestPlacementGapsAndMachineSpecification(t *testing.T) {
	repos := fixtureRepos()
	repos.Push("acme/svc", "c2", map[string]string{
		"go.mod": "module example.test/svc\n", "Containerfile": "FROM harbor.test/base/runtime:1.0\nEXPOSE 8080\n",
		".launchway/pipeline.yaml": pipelineSvc, "k8s/deploy.yaml": k8sSvc,
		".launchway/requirements.yaml": "version: 1\narchitectures: [arm64]\ncpu: 2\nmemory: 4Gi\nstorage: 20Gi\ndevices: [gpu]\ndata: [{kind: postgres, version: '>=15'}]\nos: {kernel: '>=6.1', features: [cgroups-v2]}\n",
	})
	repos.Push("acme/worker", "c1", map[string]string{
		"go.mod": "module example.test/worker\n", "Containerfile": "FROM harbor.test/base/runtime:1.0\nEXPOSE 9090\n",
		".launchway/requirements.yaml": "version: 1\narchitectures: [arm64, amd64]\ncpu: 1\nmemory: 2Gi\nports: [9090]\n",
	})
	r := newRunner(t, repos)
	inv := fakeInv{
		ms: []MachineInfo{
			{Name: "arm-gpu", Facts: map[string]any{"arch": []any{"arm64"}, "cpu_cores": 16, "memory_bytes": 64 << 30, "storage_bytes": 500 << 30, "devices": []any{"gpu:2"}, "os": "linux", "kernel": "6.8", "features": []any{"cgroups-v2"}, "platforms": []any{"container", "vm"}}, Provider: "discovery", Owner: "@infra"},
			{Name: "x86-big", Facts: map[string]any{"arch": []any{"amd64"}, "cpu_cores": 32, "memory_bytes": 128 << 30, "storage_bytes": 1 << 40, "os": "linux", "kernel": "6.8", "features": []any{"cgroups-v2"}, "platforms": []any{"container", "vm"}}, Provider: "hypervisor"},
			{Name: "arm-small", Facts: map[string]any{"arch": []any{"arm64"}, "cpu_cores": 2, "memory_bytes": 4 << 30, "storage_bytes": 50 << 30, "os": "linux", "kernel": "5.15", "platforms": []any{"container"}}, Provider: "discovery"},
		},
		ps: []PlatformInfo{{Name: "pg-main", Kind: "database", Facts: map[string]any{"data": map[string]any{"postgres": "16"}}, Hosts: []string{"x86-big"}}},
	}
	r.Sources = append(r.Sources, InventorySource{Inv: inv}, PlacementSource{Store: r.Store})
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	ps, err := r.Store.Placement(bg, AllowAll{}, ref(KindComponent, "acme/svc:Containerfile"), When{})
	if err != nil || len(ps) != 1 {
		t.Fatalf("%+v %v", ps, err)
	}
	p := ps[0]
	if len(p.Fits) != 1 || p.Fits[0].Host.Key != "arm-gpu" || len(p.Gaps) != 0 {
		t.Fatalf("fits %d gaps %+v", len(p.Fits), p.Gaps)
	}
	// Needs arm64 + GPU + 3 kernel: take the GPU machine away: nothing satisfies it.
	inv.ms = inv.ms[1:]
	r.Sources[len(r.Sources)-2] = InventorySource{Inv: inv}
	if _, err := r.Update(bg, Scope{ScopeInstallation, ""}); err != nil {
		t.Fatal(err)
	}
	ps, _ = r.Store.Placement(bg, AllowAll{}, ref(KindComponent, "acme/svc:Containerfile"), When{})
	if len(ps[0].Fits) != 0 || len(ps[0].Gaps) == 0 {
		t.Fatalf("%+v", ps[0])
	}
	var msgs []string
	for _, g := range ps[0].Gaps {
		msgs = append(msgs, g.Message)
	}
	if !strings.Contains(strings.Join(msgs, ";"), "device gpu:1: nothing satisfies it") {
		t.Fatalf("gaps %v", msgs)
	}
	if len(ps[0].NearMisses) == 0 {
		t.Fatal("no near misses shown")
	}
	// The specification for two components that go on one machine.
	sp, err := r.Store.Specification(bg, AllowAll{}, []NodeRef{ref(KindRepository, "acme/svc"), ref(KindRepository, "acme/worker")}, SpecOptions{}, When{})
	if err != nil {
		t.Fatal(err)
	}
	if sp.Arch != "arm64" || strings.Join(sp.ArchOpts, ",") != "arm64" || sp.CPU.Required != 3000 || sp.CPU.WithHeadroom != 3750 || sp.Memory.Required != 6<<30 || sp.Storage.Required != 20<<30 {
		t.Fatalf("%+v", sp)
	}
	if len(sp.Devices) != 1 || sp.Devices[0].Kind != "gpu" || sp.OS.MinKernel != "6.1" || !contains(sp.OS.Features, "cgroups-v2") || len(sp.Data) != 1 || sp.Data[0].Kind != "postgres" || sp.Data[0].Constraint != "15" {
		t.Fatalf("%+v", sp)
	}
	ports := map[int]bool{}
	for _, p := range sp.Ports {
		ports[p.Port] = true
	}
	if !ports[8080] || !ports[9090] || len(sp.Conflicts) != 0 || sp.Platform != "container" {
		t.Fatalf("%+v", sp)
	}
	y, err := sp.YAML()
	if err != nil || !strings.Contains(y, "architecture: arm64") || !strings.Contains(y, "kind: launchway.machine-specification") {
		t.Fatalf("%v\n%s", err, y)
	}
	// Components with no architecture in common are a conflict, not a guess.
	repos.Push("acme/x86only", "c1", map[string]string{"go.mod": "module example.test/x\n", "Containerfile": "FROM scratch\n", ".launchway/requirements.yaml": "version: 1\narchitectures: [amd64]\n"})
	if _, err := r.Update(bg, Scope{ScopeRepository, "acme/x86only"}); err != nil {
		t.Fatal(err)
	}
	sp, _ = r.Store.Specification(bg, AllowAll{}, []NodeRef{ref(KindRepository, "acme/svc"), ref(KindRepository, "acme/x86only")}, SpecOptions{HeadroomPercent: 50}, When{})
	if sp.Arch != "" || len(sp.Conflicts) == 0 || !strings.Contains(sp.Conflicts[0], "no architecture") || sp.Headroom != 50 {
		t.Fatalf("%+v", sp)
	}
	// Machines are only judged for a viewer who may read them.
	ps, _ = r.Store.Placement(bg, Only{"repo:acme/svc": true}, ref(KindComponent, "acme/svc:Containerfile"), When{})
	if ps[0].Hosts != 0 {
		t.Fatalf("machines leaked: %d", ps[0].Hosts)
	}
}

type fakeReg struct{}

func (fakeReg) Entries(context.Context) ([]RegistryEntry, error) {
	return []RegistryEntry{{Image: "reg.test/tools/x", Digest: "sha256:123", Platform: "linux/amd64", Base: "alpine:3.20", PushedAt: 5}}, nil
}

type fakeInfra struct{}

func (fakeInfra) Resources(context.Context) ([]InfraResource, error) {
	return []InfraResource{{Kind: "machine", Name: "vm-1", DefinedIn: "acme/infra:main.tf", Attributes: map[string]any{"arch": []any{"amd64"}}}}, nil
}

type fakeClusters struct{}

func (fakeClusters) Workloads(context.Context) ([]ClusterWorkload, error) {
	return []ClusterWorkload{{Cluster: "k1", Namespace: "prod", Name: "x", Image: "reg.test/tools/x", Digest: "sha256:123", Node: "vm-1"}}, nil
}

func TestTypedStubsAssertWhatTheirFixturesSay(t *testing.T) {
	r := newRunner(t, NewMemRepos())
	r.Sources = []Source{RegistrySource{Reg: fakeReg{}}, InfraSource{State: fakeInfra{}}, ClusterSource{Inv: fakeClusters{}}}
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	// What changes if the alpine base image changes: the registry's image, then what runs it, on which machine.
	im, err := r.Store.Impact(bg, AllowAll{}, ref(KindImage, "docker.io/library/alpine"), Limits{}, When{})
	if err != nil || im.Total < 2 {
		t.Fatalf("%+v %v", im, err)
	}
	var kinds []string
	for k := range im.ByKind {
		kinds = append(kinds, k)
	}
	if !contains(kinds, "built") || !contains(kinds, "deployment") {
		t.Fatalf("%v", im.ByKind)
	}
	// A machine that changes affects what runs on it.
	mi, _ := r.Store.Impact(bg, AllowAll{}, ref(KindMachine, "vm-1"), Limits{}, When{})
	if mi.ByKind["deployment"] != 1 {
		t.Fatalf("%+v", mi.ByKind)
	}
	// And what configures it is upstream.
	c, _ := r.Store.Contents(bg, AllowAll{}, ref(KindMachine, "vm-1"), Limits{}, When{})
	if len(c.Repositories) != 1 || c.Repositories[0].Node.Key != "acme/infra" {
		t.Fatalf("%+v", c)
	}
}
