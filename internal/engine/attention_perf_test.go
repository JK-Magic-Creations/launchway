package engine

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// With 5,000 pipelines and 200,000 Sorties, one attention answer (reading
// the facts and applying the rule) and one Sortie search stay well under 300 ms
// of server time. The numbers are logged; set LAUNCHWAY_TEST_POSTGRES to run on
// PostgreSQL too (testdb does). Under the race detector the seeding and the answers still run and
// their results are checked, but the 300 ms limits are not asserted (testlib.SkipTimingUnderRace):
// the detector made them 24 and 36 times slower on a hosted runner. In a normal build the limit is
// 300 ms on a machine as fast as the reference one, and testlib.MachineFactor stretches it (at most
// 8 times) on a slower or CPU-capped one, which the log line says.
func TestAttentionAndSearchAtScale(t *testing.T) {
	if testing.Short() || os.Getenv("LAUNCHWAY_SKIP_SCALE") != "" {
		t.Skip("scale test")
	}
	e, clk := newEngine(t)
	now := clk.Now().UnixMilli()
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	const projects, perProject = 5000, 40
	n := 0
	for p := 0; p < projects; p++ {
		for i := 1; i <= perProject; i++ {
			n++
			status := "succeeded"
			switch {
			case i == perProject && p%50 == 0:
				status = "failed"
			case i == perProject && p%97 == 0:
				status = "running"
			case i%9 == 0:
				status = "failed"
			}
			branch := "main"
			if i%3 == 0 {
				branch = fmt.Sprintf("feat-%d", i%7)
			}
			at := now - int64(projects*perProject-n)*int64(time.Second/time.Millisecond)*2
			if _, err := tx.Exec(`INSERT INTO runs(id, project, number, pipeline, status, event, ref, branch, sha, actor, created_at, started_at, finished_at, last_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				fmt.Sprintf("run_%d", n), fmt.Sprintf("proj-%04d", p), i, "ci", status, "push", "refs/heads/"+branch, branch, fmt.Sprintf("%08x", n*7919), "ana", at, at, at+300000, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d Sorties of %d pipelines", n, projects)
	t.Logf("machine factor %.2f (1 = the reference machine), limits %v", testlib.MachineFactor(), testlib.Scaled(300*time.Millisecond))
	start := time.Now()
	runs, deps, err := e.AttentionFacts(nil)
	if err != nil {
		t.Fatal(err)
	}
	read := time.Since(start)
	res := ComputeAttention(AttentionInput{Runs: runs, Deployments: deps, Now: now, User: "ana", DefaultBranch: func(string) string { return "main" }})
	total := time.Since(start)
	t.Logf("attention: read %d runs in %v, rule in %v, total %v; %d need a person, %d retrying, %d set aside", len(runs), read, total-read, total, len(res.Needs), len(res.Retrying), res.Superseded)
	if total > testlib.Scaled(300*time.Millisecond) && !testlib.SkipTimingUnderRace() {
		t.Errorf("attention took %v, want under 300ms", total)
	}
	start = time.Now()
	found, err := e.SearchRuns(nil, "#4000", 12)
	if err != nil {
		t.Fatal(err)
	}
	byBranch, _ := e.SearchRuns(nil, "feat-3", 12)
	d := time.Since(start)
	t.Logf("search: %d by number, %d by branch, both in %v", len(found), len(byBranch), d)
	if d > testlib.Scaled(300*time.Millisecond) && !testlib.SkipTimingUnderRace() {
		t.Errorf("search took %v, want under 300ms", d)
	}
}
