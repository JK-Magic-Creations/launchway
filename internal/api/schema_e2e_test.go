package api

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// /readyz and /api/v1/info say which schema this build knows and which the
// database is at. A running server whose database a newer release has
// migrated in a breaking way stops being ready.
func TestReadyzAndInfoReportTheSchema(t *testing.T) {
	e := setup(t, 5*time.Second)
	type schema struct{ Code, Database, Minimum int }
	var ready struct {
		Status string
		Error  string
		Schema schema
	}
	var info struct{ Schema schema }
	getJSON(t, e.srv.URL+"/readyz", &ready)
	getJSON(t, e.srv.URL+"/api/v1/info", &info)
	if ready.Status != "ok" || ready.Schema.Code == 0 || ready.Schema != info.Schema ||
		ready.Schema.Code != ready.Schema.Database || ready.Schema.Minimum > ready.Schema.Code {
		t.Fatalf("readyz %+v, info %+v", ready, info)
	}

	driver, dsn := "sqlite", "file:"+e.db
	if testdb.Postgres() {
		driver, dsn = "pgx", e.db
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	next := ready.Schema.Code + 1
	for _, key := range []string{"schema_version", "schema_min"} {
		q := `UPDATE meta SET value = '` + strconv.Itoa(next) + `' WHERE key = '` + key + `'`
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := http.Get(e.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	decodeBody(t, resp, &ready)
	if resp.StatusCode != http.StatusServiceUnavailable || ready.Status != "incompatible" ||
		!strings.Contains(ready.Error, "needs a Launchway build that knows schema "+strconv.Itoa(next)+" or newer") {
		t.Fatalf("readyz after a breaking migration: %d %+v", resp.StatusCode, ready)
	}
	// A server starting now refuses, saying the same.
	if _, err := engine.Open(engine.Options{Database: e.db, Logger: quiet()}); err == nil ||
		!strings.Contains(err.Error(), "knows schema "+strconv.Itoa(next)+" or newer") {
		t.Fatalf("starting against it: %v", err)
	}
}
