package githostaccept

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestAT_HG_18_Identities(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-18")
	if st, _, _ := c.do("GET", "/api/v1/user", nil, true); st != 401 && st != 403 {
		t.Fatalf("anonymous GET /user: status %d", st)
	}
	req, _ := http.NewRequest("GET", c.URL+"/api/v1/user", nil)
	req.Header.Set("Authorization", "token invalid-"+randHex(8))
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("an invalid token got status %d, want 401 on the first request", resp.StatusCode)
	}
	r.Saw(t, "anonymous and invalid-token API requests are refused at once (401)")
	name, _ := c.pushed(t)
	w := c.clone(t, c.repoURL(name))
	cfgText := c.must(t, w, "config", "--list", "--show-origin")
	_, body, _ := c.do("GET", "/api/v1/user", nil, false)
	if strings.Contains(cfgText+string(body), c.Token) {
		t.Fatal("the token appears in a clone's Git configuration or an API answer")
	}
	r.Saw(t, "the token is in no clone configuration and no API answer")
	r.NotAutomated(t, "Vault path inventory; secret scan of Launchway logs, Hubble export, run and audit logs with real token values; a revoked token failing on the next request (needs a second token)")
}

func TestAT_HG_19_Network(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-19")
	for _, p := range []string{"/api/healthz", "/api/v1/version"} {
		st, b, _ := c.do("GET", p, nil, true)
		if st != 200 {
			t.Fatalf("GET %s: status %d", p, st)
		}
		r.Saw(t, "GET %s -> 200 %.60s", p, strings.TrimSpace(string(b)))
	}
	name, _ := c.pushed(t)
	if c.Alt == "" {
		r.NotAutomated(t, "second address: set LW_ACCEPT_URL_ALT (git.home.jk-magic-creations.com)")
	} else {
		alt := &Config{URL: c.Alt, User: c.User, Token: c.Token}
		d := filepath.Join(t.TempDir(), "alt")
		if out, err := alt.git("", true, "clone", "-q", alt.repoURL(name), d); err != nil {
			t.Fatalf("clone through %s: %v %s", c.Alt, err, alt.scrub(out))
		}
		r.Saw(t, "clone through the second address %s works", c.Alt)
	}
	if c.BigMB >= 200 {
		w := c.clone(t, c.repoURL(name))
		r.Saw(t, "%d MB push accepted in %s", c.BigMB, c.bigPush(t, w, c.repoURL(name), c.BigMB).Round(time.Second))
	} else {
		r.NotAutomated(t, "the 200 MB push (set LW_ACCEPT_BIGPUSH_MB=200)")
	}
	r.NotAutomated(t, "12 namespaces admitted and a 13th refused; the same after a node reboot (kubectl run)")
}

// getJSON reads an API path into out and fails unless it answers 200.
func (c *Config) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	c.api(t, "GET", path, nil, out, 200)
}

func TestAT_HG_20_ReadAPI(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-20")
	name, seed := c.pushed(t)
	rp := c.repoPath(name)
	var v struct{ Version string }
	c.getJSON(t, "/version", &v)
	var u struct{ Login string }
	c.getJSON(t, "/user", &u)
	var s struct{ Data []struct{ Name string } }
	c.getJSON(t, "/repos/search?q="+name, &s)
	found := false
	for _, d := range s.Data {
		found = found || d.Name == name
	}
	var commits []struct{ Sha string }
	c.getJSON(t, rp+"/commits?limit=5", &commits)
	head := strings.TrimSpace(c.must(t, seed, "rev-parse", "main"))
	var file struct{ Type, Content, Encoding string }
	c.getJSON(t, rp+"/contents/README.md?ref=main", &file)
	body, _ := base64.StdEncoding.DecodeString(file.Content)
	var dir []struct{ Name string }
	c.getJSON(t, rp+"/contents", &dir)
	var br []struct{ Name string }
	c.getJSON(t, rp+"/branches", &br)
	if v.Version == "" || u.Login != c.User || !found || len(commits) == 0 || commits[0].Sha != head || string(body) != "# acceptance\n" || len(dir) < 3 || len(br) != 2 {
		t.Fatalf("surface mismatch: version %q user %q found %v commits %d head ok %v readme %q dir %d branches %d", v.Version, u.Login, found, len(commits), len(commits) > 0 && commits[0].Sha == head, body, len(dir), len(br))
	}
	r.Saw(t, "version %s; user %s; repos/search finds the repository; newest commit %.12s; README content decodes; %d root entries; %d branches", v.Version, u.Login, head, len(dir), len(br))
	r.NotAutomated(t, "Beacon Deliveries and Security for five known commits, mcp-accounts, jkma-sandbox, Homepage, Blackbox; the existing host scaled to zero for an hour")
}

func TestAT_HG_24_BackupRestore(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-24")
	name, seed := c.pushed(t)
	wantRefs, wantObjs := c.fingerprint(t, seed)
	mirror := filepath.Join(t.TempDir(), "backup.git")
	start := time.Now()
	c.must(t, "", "clone", "-q", "--mirror", c.repoURL(name), mirror)
	bundle := filepath.Join(t.TempDir(), "backup.bundle")
	c.must(t, mirror, "bundle", "create", "-q", bundle, "--all")
	c.must(t, mirror, "bundle", "verify", bundle)
	restored := c.newRepo(t)
	staged := filepath.Join(t.TempDir(), "restore.git")
	c.must(t, "", "clone", "-q", "--mirror", bundle, staged)
	c.must(t, staged, "push", "-q", "--mirror", c.repoURL(restored))
	took := time.Since(start)
	check := filepath.Join(t.TempDir(), "check.git")
	c.must(t, "", "clone", "-q", "--mirror", c.repoURL(restored), check)
	gotRefs, gotObjs := c.fingerprint(t, check)
	if gotRefs != wantRefs || gotObjs != wantObjs {
		t.Fatalf("restored repository differs\nrefs want:\n%s\ngot:\n%s\nobjects want %s got %s", wantRefs, gotRefs, wantObjs, gotObjs)
	}
	c.must(t, check, "fsck", "--full", "--strict")
	r.Saw(t, "backup (bundle, verified) restored into a second repository: %d refs and objects %s byte-identical, fsck clean, %s", len(strings.Split(gotRefs, "\n")), gotObjs, took.Round(time.Millisecond))
	r.NotAutomated(t, "host-side backup and restore of the whole instance, the drill on a scratch namespace, 1,000 kill-injections, the off-node copy, the 26 h and 8 day alerts")
}

func TestAT_HG_25_ProbeAndMetrics(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-25")
	var worst time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		if st, _, _ := c.do("GET", "/api/v1/version", nil, true); st != 200 {
			t.Fatalf("health probe status %d", st)
		}
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	if worst > time.Second {
		t.Fatalf("health probe took %s", worst)
	}
	r.Saw(t, "health route /api/v1/version: 5 probes, slowest %s (limit 1 s)", worst.Round(time.Millisecond))
	st, b, _ := c.do("GET", "/metrics", nil, true)
	lines := len(strings.Split(string(b), "\n"))
	switch {
	case st == 200 && lines > 5:
		r.Saw(t, "/metrics exposes %d lines", lines)
	case c.Launchway():
		t.Fatalf("Launchway must expose /metrics: status %d", st)
	default:
		r.Saw(t, "/metrics answers %d (required of Launchway only: the existing host's are off)", st)
	}
	r.NotAutomated(t, "ServiceProbeDown within 5 minutes of killing the pods; Homepage tile; BackupJobStale and RestoreDrillFailed names (Prometheus)")
}

func p95(d []time.Duration) time.Duration {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[(len(d)*95+99)/100-1]
}

func TestAT_HG_26_Latency(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-26")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	var clones, pushes []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		c.clone(t, url)
		clones = append(clones, time.Since(start))
		c.commit(t, seed, fmt.Sprintf("p%d.txt", i), "x\n", "latency")
		start = time.Now()
		c.must(t, seed, "push", "-q", url, "main")
		pushes = append(pushes, time.Since(start))
	}
	pc, pp := p95(clones), p95(pushes)
	if pc > 2*time.Second || pp > time.Second {
		t.Fatalf("p95 clone %s (limit 2 s), p95 push %s (limit 1 s)", pc, pp)
	}
	r.Saw(t, "20 clones p95 %s (limit 2 s); 20 pushes p95 %s (limit 1 s)", pc.Round(time.Millisecond), pp.Round(time.Millisecond))
	r.NotAutomated(t, "steady memory under 512 Mi and no OOM kill over the soak (read from Prometheus)")
}

func (c *Config) download(t *testing.T, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", c.URL+path, nil)
	req.Header.Set("Authorization", "token "+c.Token)
	resp, err := httpc.Do(req)
	if err != nil {
		t.Fatal(c.scrub(err.Error()))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	return b
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tr := tar.NewReader(zr); ; {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name[strings.Index(h.Name+"/", "/")+1:])
		}
	}
	sort.Strings(names)
	return names
}

func TestAT_HG_30_Browsing(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-30")
	name, seed := c.pushed(t)
	rp := c.repoPath(name)
	var tree struct{ Tree []struct{ Path string } }
	c.getJSON(t, rp+"/git/trees/main?recursive=true", &tree)
	var cmp struct {
		Commits []any `json:"commits"`
	}
	c.getJSON(t, rp+"/compare/v0.1...main", &cmp)
	if len(tree.Tree) != 5 || len(cmp.Commits) < 2 {
		t.Fatalf("tree has %d entries (want 5), compare v0.1...main lists %d commits", len(tree.Tree), len(cmp.Commits))
	}
	r.Saw(t, "tree of main: %d entries; compare v0.1...main: %d commits", len(tree.Tree), len(cmp.Commits))
	raw := c.download(t, "/"+c.User+"/"+name+"/raw/branch/main/README.md")
	if string(raw) != "# acceptance\n" {
		t.Fatalf("raw README is %q", raw)
	}
	r.Saw(t, "raw download of README.md equals the pushed file")
	want := strings.Fields(c.must(t, seed, "ls-tree", "-r", "--name-only", "main"))
	sort.Strings(want)
	got := tarNames(t, c.download(t, "/"+c.User+"/"+name+"/archive/main.tar.gz"))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("archive holds %v, repository has %v", got, want)
	}
	zb := c.download(t, "/"+c.User+"/"+name+"/archive/main.zip")
	if _, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb))); err != nil {
		t.Fatal(err)
	}
	r.Saw(t, "tar.gz archive lists the same %d files as git ls-tree; zip archive opens", len(got))
	var s struct{ Data []struct{ Name string } }
	c.getJSON(t, "/repos/search?q="+name, &s)
	if len(s.Data) != 1 {
		t.Fatalf("search for %s found %d repositories", name, len(s.Data))
	}
	r.Saw(t, "repository search by name finds exactly this repository")
	r.NotAutomated(t, "the UI pages (tree, blob, commits) in a browser, three real repositories")
}

// TestHomelabOnly records the acceptance tests this suite does not run, each
// with the reason, so a report never lists a test as passed by omission.
func TestHomelabOnly(t *testing.T) {
	host(t)
	for _, e := range Catalog {
		if e.Coverage != Manual {
			continue
		}
		t.Run(e.ID, func(t *testing.T) {
			r := begin(t, e.ID)
			r.skip(t, "not automated: "+e.Why)
		})
	}
}
