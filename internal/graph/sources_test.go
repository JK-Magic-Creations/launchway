package graph

import (
	"strings"
	"testing"
)

func TestManifestsResolveAcrossRepositories(t *testing.T) {
	r := newRunner(t, fixtureRepos())
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	ord, err := r.Store.Order(bg, AllowAll{}, OrderQuery{})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, n := range ord.Order {
		pos[n] = i
	}
	for _, p := range [][2]string{{"acme/libgo", "acme/svc"}, {"acme/base", "acme/svc"}, {"acme/ui", "acme/web"}, {"acme/pylib", "acme/pyapp"}} {
		a, aok := pos[p[0]]
		b, bok := pos[p[1]]
		if !aok || !bok || a >= b {
			t.Errorf("%s must come before %s: %v", p[0], p[1], ord.Order)
		}
	}
	var viaPkg, viaImg bool
	for _, e := range ord.Edges {
		if e.Consumer == "acme/svc" && e.Provider == "acme/libgo" && strings.Contains(strings.Join(e.Via, ","), "go:example.test/libgo") && len(e.Versions) > 0 {
			viaPkg = true
		}
		if e.Consumer == "acme/svc" && e.Provider == "acme/base" && strings.Join(e.Via, ",") == "harbor.test/base/runtime" {
			viaImg = true
		}
	}
	if !viaPkg || !viaImg || len(ord.Cycles) != 0 {
		t.Fatalf("edges %+v cycles %+v", ord.Edges, ord.Cycles)
	}
	// Levels: the libraries are built together, first.
	if len(ord.Levels) < 2 || !contains(ord.Levels[0], "acme/libgo") || !contains(ord.Levels[0], "acme/base") {
		t.Fatalf("levels %v", ord.Levels)
	}
}

func TestImageSourceStagesAndPlatforms(t *testing.T) {
	r := newRunner(t, fixtureRepos())
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	// Both stages' bases are edges, with the stage on each.
	edges, _, err := r.Store.Edges(bg, AllowAll{}, ref(KindComponent, "acme/svc:Containerfile"), When{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, e := range edges {
		if e.Kind == EdgeBuiltFrom && e.From.Kind == KindComponent {
			stages = append(stages, e.To.Key+"|"+e.Qualifier+"|"+e.Constraint)
		}
	}
	want := "docker.io/library/golang|stage:build|1.23 harbor.test/base/runtime|stage:1|1.0"
	got := strings.Join(sortedStrings(stages), " ")
	if got != want {
		t.Fatalf("stages\n got %s\nwant %s", got, want)
	}
	n, err := r.Store.Get(bg, AllowAll{}, ref(KindImage, "harbor.test/apps/svc"), When{})
	if err != nil || strings.Join(strs(n.Attrs["platforms"]), ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("%+v %v", n, err)
	}
}

func sortedStrings(s []string) []string {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return sortedKeys(m)
}

func TestImpactOfABaseImageReachesTheWholeChain(t *testing.T) {
	r := newRunner(t, fixtureRepos())
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	im, err := r.Store.Impact(bg, AllowAll{}, ref(KindImage, "docker.io/library/debian"), Limits{}, When{})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, g := range im.ByOwner {
		for _, it := range g.Items {
			keys[string(it.Node.Kind)+":"+it.Node.Key] = true
		}
	}
	for _, k := range []string{"component:acme/base:Containerfile", "image:harbor.test/base/runtime", "component:acme/svc:Containerfile", "image:harbor.test/apps/svc"} {
		if !keys[k] {
			t.Errorf("%s not reached: %v", k, keys)
		}
	}
	// Grouped by owner: each repository's owner from CODEOWNERS.
	var owners []string
	for _, g := range im.ByOwner {
		owners = append(owners, g.Owner)
	}
	if !contains(owners, "@platform-team") || !contains(owners, "@apps-team") {
		t.Fatalf("owners %v", owners)
	}
}

func TestRequirementsDeclaredInferredAndAdvice(t *testing.T) {
	repos := fixtureRepos()
	repos.Push("acme/svc", "c-svc-2", map[string]string{
		"go.mod":                   "module example.test/svc\n\ngo 1.23\n\nrequire github.com/jackc/pgx/v5 v5.5.0\n",
		"Containerfile":            "FROM harbor.test/base/runtime:1.0\nEXPOSE 8080\n",
		".launchway/pipeline.yaml": pipelineSvc,
		"k8s/deploy.yaml":          k8sSvc,
		".launchway/requirements.yaml": `version: 1
architectures: [arm64]
memory: 1Gi
devices: [gpu]
data:
  - { kind: postgres, version: ">=15" }
`,
	})
	r := newRunner(t, repos)
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	ns, err := r.Store.Needs(bg, AllowAll{}, ref(KindRepository, "acme/svc"), When{})
	if err != nil || len(ns) != 1 {
		t.Fatalf("%+v %v", ns, err)
	}
	by := map[string]Fact{}
	for _, f := range ns[0].Facts {
		by[f.Facet+"="+f.Value] = f
	}
	// Declared wins: arm64 and 1Gi; the pipeline's amd64 is advice, the manifest's 512Mi too.
	if f, ok := by["arch=arm64"]; !ok || f.Source != Declared {
		t.Fatalf("facts %+v", ns[0].Facts)
	}
	if _, ok := by["arch=amd64"]; ok {
		t.Fatal("an inferred architecture overrode the declared one")
	}
	if f := by["memory=1073741824"]; f.Source != Declared {
		t.Fatalf("memory %+v", ns[0].Facts)
	}
	// Inferred where the file is silent.
	if f := by["cpu=500"]; f.Source != Inferred || !strings.Contains(f.Evidence, "Deployment/svc") {
		t.Fatalf("cpu %+v", by)
	}
	if f := by["port=8080/tcp"]; f.Source != Inferred {
		t.Fatalf("port %+v", by)
	}
	if f := by["runtime=go>=1.23"]; f.Source != Inferred {
		t.Fatalf("runtime %+v", by)
	}
	var adv []string
	for _, a := range ns[0].Advice {
		adv = append(adv, a.Facet+":"+a.Inferred)
	}
	if !contains(adv, "arch:amd64") || !contains(adv, "memory:512Mi") {
		t.Fatalf("advice %v", adv)
	}
	// A file with a mistake is refused, and what the graph holds stays.
	repos.Push("acme/svc", "c-svc-3", map[string]string{"go.mod": "module example.test/svc\n", "Containerfile": "FROM harbor.test/base/runtime:1.0\n", ".launchway/requirements.yaml": "version: 1\nmemory: lots\n"})
	res, _ := r.Update(bg, Scope{ScopeRepository, "acme/svc"})
	if !strings.Contains(res.Errors["requirements"], "memory") {
		t.Fatalf("%+v", res)
	}
	if ns2, _ := r.Store.Needs(bg, AllowAll{}, ref(KindRepository, "acme/svc"), When{}); len(ns2) == 0 || len(ns2[0].Facts) == 0 {
		t.Fatal("a broken file wiped the requirements")
	}
}

func TestParseRequirementsRefusesMistakes(t *testing.T) {
	for in, want := range map[string]string{
		"version: 2\n":                                 "version must be 1",
		"version: 1\nplatform: toaster\n":              "platform",
		"version: 1\ncpu: many\n":                      "cpu",
		"version: 1\nports: [99999]\n":                 "ports",
		"version: 1\ndata: [jello]\n":                  "data",
		"version: 1\nbogus: 1\n":                       "bogus",
		"version: 1\narchitectures: [x86]\n":           "architectures",
		"":                                             "empty",
		"version: 1\ncomponents:\n  a: {path: ../x}\n": "path",
	} {
		if _, err := ParseRequirements([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
	r, err := ParseRequirements([]byte("version: 1\ncpu: 500m\ndevices: [gpu, {kind: tpu, count: 2}]\ndata: [redis]\ncomponents:\n  web: {path: web, memory: 2Gi}\n"))
	if err != nil || r.Components["web"].Memory != "2Gi" || r.Devices[0].Kind != "gpu" || r.Data[0].Kind != "redis" {
		t.Fatalf("%+v %v", r, err)
	}
}
