package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 32 is migrated to 35 with the startup guard on: the
// cache table carries the new columns, entries written before stay (and are
// never read by a run), and a build of schema 32 starts against the newer
// database and carries on.
func TestUpgradeFromSchema32ToCacheTrustAndBack(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old, err := openAs(t, addr, clk, schemaRollback, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_old', 'p', 'default', 'deps', 'k', 'linux/amd64', 'caches/old', 'kept')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaStandIn33, schemaStandIn34, schemaCacheTrust} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var pipeline string
	var gen, sens int
	if err := cur.db.QueryRow(`SELECT pipeline, generation, sensitive FROM caches WHERE id = 'cch_old'`).Scan(&pipeline, &gen, &sens); err != nil || pipeline != "" || gen != 1 || sens != 0 {
		t.Fatalf("the old entry: %q %d %d %v", pipeline, gen, sens, err)
	}
	// Two generations of one key are allowed now; the same generation twice is not.
	ins := func(id string, g int) error {
		_, err := cur.db.Exec(`INSERT INTO caches(id, project, scope, pipeline, name, key, platform, blob_key, status, generation) VALUES(?, 'p', 'default', 'ci', 'deps', 'k', 'linux/amd64', ?, 'kept', ?)`, id, "caches/"+id, g)
		return err
	}
	if err := ins("cch_g1", 1); err != nil {
		t.Fatal(err)
	}
	if err := ins("cch_g2", 2); err != nil {
		t.Fatalf("a second generation of a key: %v", err)
	}
	if err := ins("cch_g2b", 2); err == nil {
		t.Fatal("the same generation of a key was stored twice")
	}
	cur.Close()
	older, err := openAs(t, addr, clk, schemaRollback, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 32 on a database of schema 35: %v", err)
	}
	older.Close()
}
