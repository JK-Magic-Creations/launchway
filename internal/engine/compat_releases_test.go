package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The two releases before this one keep working against a database this build has migrated to schema 68: the
// build of 23f74683 knows schema 56 and the build of 8de9d372 knows schema 62. Each starts against the database,
// says once that it is newer, and runs a run end to end; the newer build then reads that run as finished. Every
// migration from 57 to 68 is additive, which is what lets a rolling update or a rollback of the server go back.
func TestPreviousReleaseBuildsRunAgainstSchema68(t *testing.T) {
	if schemaOutside != 56 || schemaSizing != 62 || schemaLast != 68 || len(migrations) != 68 {
		t.Fatalf("the schemas are outside %d, sizing %d, last %d (%d migrations); this test names 56, 62 and 68", schemaOutside, schemaSizing, schemaLast, len(migrations))
	}
	for v := 57; v <= 68; v++ {
		if migrations[v-1].compat != additive {
			t.Fatalf("migration %d is breaking: the previous releases could not run against schema 68", v)
		}
	}
	for _, build := range []struct {
		commit string
		schema int
	}{{"23f74683", schemaOutside}, {"8de9d372", schemaSizing}} {
		t.Run(build.commit, func(t *testing.T) {
			clk := &clock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
			addr := testdb.New(t)
			var newLog syncBuffer
			cur := mustOpenAs(t, addr, clk, 0, &newLog)
			if info, err := cur.Schema(context.Background()); err != nil || info.Database != 68 || info.Code != 68 {
				t.Fatalf("the current build's database: %+v %v", info, err)
			}
			var oldLog syncBuffer
			old := mustOpenAs(t, addr, clk, build.schema, &oldLog)
			info, err := old.Schema(context.Background())
			if err != nil || info.Code != build.schema || info.Database != 68 || info.Minimum > build.schema || !info.Compatible() {
				t.Fatalf("the build of %s sees the schema as %+v, %v", build.commit, info, err)
			}
			if c := strings.Count(oldLog.String(), "running against a database written by a newer version"); c != 1 {
				t.Fatalf("the build of %s said %d times that the database is newer:\n%s", build.commit, c, oldLog.String())
			}
			id := create(t, old, linear, Trigger{Event: "manual"})
			for _, step := range []string{"a", "b", "c"} {
				j := take(t, old)
				if j.RunID != id || j.StepID != step {
					t.Fatalf("the build of %s leased %s/%s, want %s/%s", build.commit, j.RunID, j.StepID, id, step)
				}
				if err := old.Finish(j.LeaseID, Result{Outcome: "succeeded", Outputs: map[string]string{"out": "from " + step}}); err != nil {
					t.Fatal(err)
				}
			}
			want(t, statuses(t, old, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
			want(t, statuses(t, cur, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
		})
	}
}
