package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// schemaCheckSkipEnv lets an operator start despite a schema mismatch. It
// does not bypass errors encountered while reading the database's metadata.
const schemaCheckSkipEnv = "LAUNCHWAY_SKIP_SCHEMA_CHECK"

// What each of the schemas 17 to 21 brought, which no other schema may
// have: the feature branches that wrote them each carried empty stand-ins
// for the numbers of the others, and a merge that left a stand-in where a
// real migration belongs (or the other way round) would show here. Tables
// are listed by name; columns as table.column. The migration tests and the
// startup guard share this list so their expectations cannot drift apart.
var schemaMarkers = []struct {
	schema  int
	feature string
	tables  []string
	columns []string
}{
	{schemaBlobs, "the built-in blob store", []string{"blob_uploads", "blob_parts", "blob_chunks", "blob_objects"}, []string{"artifact_blobs.store"}},
	{schemaReports, "richer test reports and coverage", []string{"coverage_reports", "coverage_files"}, []string{"test_cases.tags", "test_cases.variant", "test_cases.extra"}},
	{schemaVerify, "verify and ai steps", []string{"verifications", "verify_baselines", "ai_usage", "ai_explanations"}, []string{"steps.kind"}},
	{schemaIdentity, "runner identities", []string{"runner_join_tokens", "runner_identities", "runner_access_tokens", "runner_assertions"},
		[]string{"runners.kind", "runners.auth", "runners.labels", "runners.accept", "runners.os", "runners.arch"}},
	{schemaPools, "build runner pools", nil, []string{"runs.runner_pool", "runs.runner_labels"}},
}

// schemaTableExists reports whether the database has the table.
func schemaTableExists(d *dbx, table string) (bool, error) {
	var q string
	if d.pg {
		q = `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`
	} else {
		q = `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	}
	var n int
	if err := d.QueryRow(q, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// schemaColumnExists reports whether the database has the table's column.
func schemaColumnExists(d *dbx, table, column string) (bool, error) {
	var q string
	if d.pg {
		q = `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`
	} else {
		q = `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`
	}
	var n int
	if err := d.QueryRow(q, table, column).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

type missingSchemaMarker struct {
	schema  int
	feature string
	tables  []string
	columns []string
}

// checkSchemaMarkers refuses a database whose recorded version includes a
// migration but whose tables and columns do not include what it brought.
func checkSchemaMarkers(d *dbx, recorded int, log *slog.Logger) error {
	var missing []missingSchemaMarker
	for _, m := range schemaMarkers {
		if m.schema > recorded {
			continue
		}
		mm := missingSchemaMarker{schema: m.schema, feature: m.feature}
		for _, table := range m.tables {
			ok, err := schemaTableExists(d, table)
			if err != nil {
				return fmt.Errorf("checking schema %d table %s: %w", m.schema, table, err)
			}
			if !ok {
				mm.tables = append(mm.tables, table)
			}
		}
		for _, name := range m.columns {
			table, column, _ := strings.Cut(name, ".")
			ok, err := schemaColumnExists(d, table, column)
			if err != nil {
				return fmt.Errorf("checking schema %d column %s: %w", m.schema, name, err)
			}
			if !ok {
				mm.columns = append(mm.columns, name)
			}
		}
		if len(mm.tables) > 0 || len(mm.columns) > 0 {
			missing = append(missing, mm)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	msg := schemaMarkerRefusal(recorded, missing)
	if os.Getenv(schemaCheckSkipEnv) == "1" {
		log.Warn("starting despite an incomplete database schema; runs may fail later", "schema", recorded,
			"missing", schemaMarkerMissing(missing), "reason", schemaCheckSkipEnv+"=1")
		return nil
	}
	return errors.New(msg)
}

func schemaMarkerMissing(missing []missingSchemaMarker) string {
	var details []string
	for _, m := range missing {
		var objects []string
		if len(m.tables) == 1 {
			objects = append(objects, "missing table "+m.tables[0])
		} else if len(m.tables) > 1 {
			objects = append(objects, "missing tables "+strings.Join(m.tables, ", "))
		}
		if len(m.columns) == 1 {
			objects = append(objects, "missing column "+m.columns[0])
		} else if len(m.columns) > 1 {
			objects = append(objects, "missing columns "+strings.Join(m.columns, ", "))
		}
		details = append(details, fmt.Sprintf("schema %d (%s): %s", m.schema, m.feature, strings.Join(objects, "; ")))
	}
	return strings.Join(details, "; ")
}

func schemaMarkerRefusal(recorded int, missing []missingSchemaMarker) string {
	return fmt.Sprintf("database schema check failed: recorded schema %d does not match its tables and columns: %s. "+
		"Restore the database from a known-good backup, or recreate only the listed objects with the corresponding migration SQL in internal/engine/db.go; "+
		"do not change meta.schema_version to hide the mismatch. To start anyway, set %s=1 (unsafe: runs may fail later)",
		recorded, schemaMarkerMissing(missing), schemaCheckSkipEnv)
}
