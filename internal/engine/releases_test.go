package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	rls "github.com/jk-magic-creations/launchway/internal/release"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func newReleaseEngine(t *testing.T, envs []Environment) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: envs, Artifacts: ArtifactOptions{Store: store}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, clk
}

const sha1c = "1111111111111111111111111111111111111111"

func draft(t *testing.T, e *Engine, project, version, sha string) *Release {
	t.Helper()
	r, err := e.CreateRelease(NewRelease{Project: project, Version: version, SHA: sha, Notes: "notes for " + version, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func publish(t *testing.T, e *Engine, id, by string) *Release {
	t.Helper()
	r, err := e.PublishRelease(id, PublishInput{Tag: "v" + id[len(id)-6:], TagObject: "t" + id, TagCreated: true, Actor: by})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func conflict(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("%s: want a conflict, got %v", what, err)
	}
}

// A published release never changes: not its notes, assets, tag or commit; it is only withdrawn, with a reason.
func TestPublishedReleaseIsImmutable(t *testing.T) {
	e, _ := newReleaseEngine(t, nil)
	ctx := context.Background()
	r := draft(t, e, "shop", "1.0.0", sha1c)
	content := []byte("the binary of 1.0.0")
	a, err := e.AddReleaseFile(ctx, r.ID, "shop-linux-amd64", "", bytes.NewReader(content), Provenance{}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if a.SHA256 != hex.EncodeToString(sum[:]) || a.Size != int64(len(content)) {
		t.Fatalf("checksum or size: %+v", a)
	}
	if _, err := e.AddReleaseFile(ctx, r.ID, "bad", "deadbeef", strings.NewReader("x"), Provenance{}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a wrong checksum is refused: %v", err)
	}
	if _, err := e.AddReleaseFile(ctx, r.ID, "shop-linux-amd64", "", strings.NewReader("other"), Provenance{}, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("an asset is not replaced: %v", err)
	}
	if _, err := e.AddReleaseFile(ctx, r.ID, "../etc/passwd", "", strings.NewReader("x"), Provenance{}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a path in a name: %v", err)
	}
	if _, err := e.MakeCandidate(r.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	p := publish(t, e, r.ID, "ann")
	if !p.Immutable || p.State != ReleasePublished || p.PublishedBy != "ann" || len(p.Assets) != 1 {
		t.Fatalf("published: %+v", p)
	}
	rc, _, err := e.OpenReleaseAsset(ctx, r.ID, "shop-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("the stored content differs")
	}
	// Every way of changing it is a conflict.
	notes := "new"
	_, err = e.EditRelease(r.ID, nil, &notes)
	conflict(t, err, "edit notes")
	_, err = e.AddReleaseFile(ctx, r.ID, "late", "", strings.NewReader("x"), Provenance{}, "ann")
	conflict(t, err, "add asset")
	conflict(t, e.RemoveReleaseAsset(ctx, r.ID, "shop-linux-amd64"), "remove asset")
	conflict(t, e.DeleteRelease(r.ID), "delete")
	_, err = e.PublishRelease(r.ID, PublishInput{Tag: "v2", Actor: "ann"})
	conflict(t, err, "publish again")
	_, err = e.MakeCandidate(r.ID, "ann")
	conflict(t, err, "back to candidate")
	if err := e.SetReleaseNotes(r.ID, "x", "y"); !errors.Is(err, ErrConflict) {
		t.Fatal("notes cannot be replaced")
	}
	if _, err := e.SetReleaseSettings(ReleaseSettings{Project: "shop", Scheme: rls.SchemeCalver, TagPrefix: "v", Changelog: "CHANGELOG.md"}, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the scheme cannot change under published releases: %v", err)
	}
	// Withdrawal needs a reason and keeps the record.
	if _, err := e.YankRelease(r.ID, " ", "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatal("a reason is required")
	}
	y, err := e.YankRelease(r.ID, "a crash on start", "bob")
	if err != nil || y.State != ReleaseYanked || y.YankReason != "a crash on start" || y.YankedBy != "bob" || len(y.Assets) != 1 || y.Tag != p.Tag || y.SHA != sha1c {
		t.Fatalf("yanked: %+v %v", y, err)
	}
}

func TestVersionRulesOfReleases(t *testing.T) {
	e, _ := newReleaseEngine(t, nil)
	r := draft(t, e, "shop", "1.4.0", sha1c)
	publish(t, e, r.ID, "ann")
	if _, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.3.9", SHA: sha1c}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a lower version is refused: %v", err)
	}
	if m, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.3.9", SHA: sha1c, AllowLower: true}); err != nil || m.Previous != "" {
		t.Fatalf("a maintenance release is allowed when asked for: %+v %v", m, err)
	}
	if _, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.4.0", SHA: sha1c}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the same version twice: %v", err)
	}
	if _, err := e.CreateRelease(NewRelease{Project: "shop", Version: "v1.5", SHA: sha1c}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a semantic version: %v", err)
	}
	if _, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.5.0", SHA: "nothex"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a commit is needed: %v", err)
	}
	pre, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.5.0-rc.1", SHA: sha1c})
	if err != nil || !pre.Prerelease || pre.Previous != "1.4.0" {
		t.Fatalf("pre-release: %+v %v", pre, err)
	}
	hi, _, _ := e.HighestRelease("shop")
	if hi != "1.4.0" { // drafts do not count
		t.Fatalf("highest is %s", hi)
	}
	// Another scheme, with its own order.
	if _, err := e.SetReleaseSettings(ReleaseSettings{Project: "cal", Scheme: rls.SchemeCalver, TagPrefix: "", Changelog: "CHANGELOG.md"}, "ann"); err != nil {
		t.Fatal(err)
	}
	c := draft(t, e, "cal", "2026.11.3", sha1c)
	publish(t, e, c.ID, "ann")
	if _, err := e.CreateRelease(NewRelease{Project: "cal", Version: "2026.11.21", SHA: sha1c}); err != nil {
		t.Fatalf("calendar order compares numbers: %v", err)
	}
	if _, err := e.CreateRelease(NewRelease{Project: "cal", Version: "1.2.3", SHA: sha1c}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a calendar version starts with a year: %v", err)
	}
}

// What a release is promoted with is what its Sortie built: the digests are
// read from the images of the run at the release's commit, and a run of
// another commit is refused.
func TestReleaseCarriesTheDigestsOfItsSortie(t *testing.T) {
	e, _ := newReleaseEngine(t, prodEnv)
	run := createShop(t, e, shopPipeline, 1)
	sha := fmt.Sprintf("%040d", 1)
	if _, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.0.0", SHA: fmt.Sprintf("%040d", 2), RunID: run}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a Sortie of another commit is refused: %v", err)
	}
	if _, err := e.CreateRelease(NewRelease{Project: "other", Version: "1.0.0", SHA: sha, RunID: run}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a Sortie of another project is refused: %v", err)
	}
	r, err := e.CreateRelease(NewRelease{Project: "shop", Version: "1.0.0", SHA: sha, RunID: run})
	if err != nil || r.RunID != run || r.RunNumber != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	if _, err := e.db.Exec(`INSERT INTO images(run_id, step_id, attempt, project, run_number, digest, image, pushed, signed, outcome, summary, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, run, "build", 1, "shop", 1, digest, "reg.example/shop@"+digest, 1, 1, "succeeded",
		fmt.Sprintf(`{"digest":%q,"image":"reg.example/shop@%s","pushed":true,"signed":true,"sbom":true,"platforms":[],"tags":[],"builders":[],"scan":{"critical":0,"high":1,"medium":2,"low":3,"unknown":0}}`, digest, digest), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	added, err := e.AddReleaseRunOutputs(r.ID, run, "ann")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, a := range added {
		kinds[a.Kind] = true
		if a.RunNumber != 1 || a.SourceSHA != sha || a.AddedBy != "ann" {
			t.Errorf("provenance of %s: %+v", a.Name, a)
		}
	}
	if !kinds[AssetImage] || !kinds[AssetSBOM] || !kinds[AssetScan] {
		t.Fatalf("image, SBOM and scan are recorded: %+v", added)
	}
	got, _ := e.GetRelease(r.ID)
	if len(got.Digests) != 1 || got.Digests[0].Digest != digest {
		t.Fatalf("digests: %+v", got.Digests)
	}
	if again, err := e.AddReleaseRunOutputs(r.ID, run, "ann"); err != nil || len(again) != 0 {
		t.Fatalf("attaching twice adds nothing: %v %v", again, err)
	}
}

var twoEnvs = []Environment{{Name: "dev", Target: "cluster-a", Projects: []string{"shop"}}, {Name: "prod", Target: "cluster-a", Projects: []string{"shop"}, Protected: true}}

func TestChecklistWindowFreezeAndClearance(t *testing.T) {
	e, clk := newReleaseEngine(t, twoEnvs)
	r := draft(t, e, "shop", "1.0.0", sha1c)
	if _, err := e.SetReleaseGate("shop", "prod", rls.Gate{RequireNotes: true, RequireChecks: true, Clearances: 1, ClearanceRoles: []string{"operator", "admin"},
		PreviousAirfield: "dev", SoakMinutes: 30, Window: &rls.Window{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, From: "00:00", To: "23:59"}}, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetReleaseGate("shop", "nowhere", rls.Gate{}, "root"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown Airfield: %v", err)
	}
	if _, err := e.SetReleaseGate("shop", "prod", rls.Gate{Window: &rls.Window{From: "17:00", To: "09:00"}}, "root"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad window: %v", err)
	}
	ck, err := e.ReleaseChecklist(r.ID, "prod")
	if err != nil || ck.Ready {
		t.Fatalf("a draft is not ready: %+v %v", ck, err)
	}
	ids := map[string]rls.Status{}
	for _, i := range ck.Items {
		ids[i.ID] = i.Status
	}
	if ids["state"] != rls.Fail || ids["checks"] != rls.Fail || ids["previous"] != rls.Fail || ids["clearance"] != rls.Waiting {
		t.Fatalf("lines: %v", ids)
	}
	pub := publish(t, e, r.ID, "ann")
	if _, err := e.GiveReleaseClearance(pub.ID, "prod", "ann", "admin", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the publisher may not clear their own release: %v", err)
	}
	if _, err := e.GiveReleaseClearance(pub.ID, "prod", "viv", "viewer", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer's clearance does not count: %v", err)
	}
	if _, err := e.GiveReleaseClearance(pub.ID, "dev", "bob", "operator", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("dev asks for no clearance: %v", err)
	}
	if _, err := e.GiveReleaseClearance(pub.ID, "prod", "bob", "operator", "looks right"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.GiveReleaseClearance(pub.ID, "prod", "bob", "operator", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("one clearance per person: %v", err)
	}
	// A freeze stops it, and the line says why and until when.
	if _, err := e.CreateReleaseFreeze("", "prod", clk.Now().Add(-time.Hour), clk.Now().Add(time.Hour), "quarter close", "root"); err != nil {
		t.Fatal(err)
	}
	ck, _ = e.ReleaseChecklist(pub.ID, "prod")
	for _, i := range ck.Items {
		if i.ID == "freeze" && (i.Status != rls.Fail || !strings.Contains(i.Detail, "quarter close")) {
			t.Fatalf("freeze line: %+v", i)
		}
		if i.ID == "clearance" && i.Status != rls.Pass {
			t.Fatalf("clearance line: %+v", i)
		}
	}
	if ck.Ready || ck.CanOverride {
		t.Fatalf("the unmet checks and previous Airfield cannot be overridden: %+v", ck)
	}
	// Dev has no gate: only the state, and the freeze of prod does not touch it.
	ck, _ = e.ReleaseChecklist(pub.ID, "dev")
	if !ck.Ready {
		t.Fatalf("dev is open: %+v", ck.Items)
	}
	fz, _ := e.ReleaseFreezes(0)
	if len(fz) != 1 {
		t.Fatalf("freezes: %v", fz)
	}
	if err := e.CancelReleaseFreeze(fz[0].ID, "root"); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelReleaseFreeze(fz[0].ID, "root"); !errors.Is(err, ErrNotFound) {
		t.Fatal("canceled once")
	}
	// A candidate does not go to a protected Airfield.
	c := draft(t, e, "shop", "1.1.0", sha1c)
	if _, err := e.MakeCandidate(c.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	ck, _ = e.ReleaseChecklist(c.ID, "prod")
	for _, i := range ck.Items {
		if i.ID == "state" && (i.Status != rls.Fail || !strings.Contains(i.Detail, "protected")) {
			t.Fatalf("candidate to protected: %+v", i)
		}
	}
	ck, _ = e.ReleaseChecklist(c.ID, "dev")
	if !ck.Ready {
		t.Fatalf("a candidate may go to dev: %+v", ck.Items)
	}
}

// The matrix reads what is where from the deployments, and the report works
// the measures from them.
func TestMatrixAndMeasuresFromDeployments(t *testing.T) {
	e, clk := newReleaseEngine(t, prodEnv)
	r1 := draft(t, e, "shop", "1.3.2", fmt.Sprintf("%040d", 1))
	if _, err := e.db.Exec(`UPDATE releases SET commit_time = ? WHERE id = ?`, clk.Now().Add(-5*time.Hour).UnixMilli(), r1.ID); err != nil {
		t.Fatal(err)
	}
	publish(t, e, r1.ID, "ann")
	shopDone(t, e, 1, "1.3.2", "c1")
	clk.Add(time.Hour)
	r2 := draft(t, e, "shop", "1.4.0", fmt.Sprintf("%040d", 2))
	publish(t, e, r2.ID, "ann")
	shopDone(t, e, 2, "1.4.0", "c2")
	afs, rows, err := e.ReleaseMatrix(nil)
	if err != nil || len(afs) != 1 || afs[0] != "prod" || len(rows) != 1 {
		t.Fatalf("%v %v %v", afs, rows, err)
	}
	c := rows[0].Cells[0]
	if c.Version != "1.4.0" || c.ReleaseID != r2.ID || c.Health != "healthy" || rows[0].Latest != "1.4.0" || c.Since == 0 {
		t.Fatalf("cell: %+v row: %+v", c, rows[0])
	}
	if _, rows, _ := e.ReleaseMatrix(func(string) bool { return false }); len(rows) != 0 {
		t.Fatal("a project the caller may not see is not shown")
	}
	ls, err := e.ReleaseLaunches("shop", "", clk.Now().Add(-48*time.Hour), clk.Now().Add(time.Hour))
	if err != nil || len(ls) != 2 {
		t.Fatalf("launches: %+v %v", ls, err)
	}
	if ls[0].Merged.IsZero() || ls[0].Failed {
		t.Fatalf("launch: %+v", ls[0])
	}
	rep, err := e.BuildReleaseReport("shop", clk.Now().Add(-24*time.Hour), clk.Now().Add(time.Hour), nil)
	if err != nil || rep.Measures.Launches != 2 || rep.Measures.Failures != 0 || rep.Measures.LeadTimeCount != 1 {
		t.Fatalf("report: %+v %v", rep, err)
	}
	if rep.Measures.LeadTimeMedianSec < 4*3600 || rep.Measures.LeadTimeMedianSec > 6*3600 {
		t.Fatalf("lead time %v s: the commit was five hours before the launch", rep.Measures.LeadTimeMedianSec)
	}
	if _, err := e.BuildReleaseReport("shop", clk.Now(), clk.Now().Add(-time.Hour), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("a period must run forwards")
	}
}

func TestFormationsGroupInOrderAndRefuseWhatIsNotPublished(t *testing.T) {
	e, _ := newReleaseEngine(t, nil)
	a := publish(t, e, draft(t, e, "api", "2.0.0", sha1c).ID, "ann")
	w := publish(t, e, draft(t, e, "web", "5.1.0", sha1c).ID, "ann")
	d := draft(t, e, "docs", "1.0.0", sha1c)
	if _, err := e.CreateFormation("q4", "", []string{a.ID, d.ID}, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a draft is not grouped: %v", err)
	}
	if _, err := e.CreateFormation("q4", "", []string{a.ID}, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one release is not a formation: %v", err)
	}
	f, err := e.CreateFormation("q4 launch", "api first, then web", []string{a.ID, w.ID}, "ann")
	if err != nil || f.Members[0].Project != "api" || f.Members[1].Project != "web" || f.OrderSource != "explicit" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := e.CreateFormation("q4 launch", "", []string{w.ID, a.ID}, "ann"); !errors.Is(err, ErrConflict) {
		t.Fatalf("names are unique: %v", err)
	}
	got, err := e.GetFormation(f.Name)
	if err != nil || len(got.Members) != 2 || got.Members[0].ReleaseID != a.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if err := e.SetFormationMember(f.ID, a.ID, MemberLaunched, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFormationMember(f.ID, w.ID, MemberFailed, "", "the Sortie failed"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.GetFormation(f.ID)
	if got.Summary() != "1 launched, 1 failed" {
		t.Fatalf("summary: %q", got.Summary())
	}
}
