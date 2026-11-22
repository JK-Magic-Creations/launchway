package store

import (
	"context"
	"fmt"
	"strings"
)

// The second lock: PostgreSQL row-level security on every Scoped table. A
// scoped transaction sets app.base_id for itself (Env.Do); the policy lets a
// statement see and write only rows whose base_id equals it. With the setting
// unset the comparison is null and nothing matches, so a statement that
// forgot its predicate (or a connection that never set one) finds nothing.
//
// The policy is restrictive, so no other policy can widen it; a permissive
// policy that admits everything sits beside it because row security with only
// restrictive policies would admit nothing at all. The application must
// connect as a role that does not own the tables and has no BYPASSRLS: the
// owner (the migration role) is exempt by design, and FORCE covers the case
// where the application's role does own them (but never a superuser).

const (
	policyOpen = "base_open"
	policyWall = "base_wall"
)

// RLSStatements are the statements that put the wall on one table. They are
// generated from the registry's table names only.
func RLSStatements(table string) []string {
	const pred = `base_id = current_setting('app.base_id', true)`
	return []string{
		`ALTER TABLE ` + table + ` ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE ` + table + ` FORCE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS ` + policyOpen + ` ON ` + table,
		`CREATE POLICY ` + policyOpen + ` ON ` + table + ` AS PERMISSIVE FOR ALL USING (true) WITH CHECK (true)`,
		`DROP POLICY IF EXISTS ` + policyWall + ` ON ` + table,
		`CREATE POLICY ` + policyWall + ` ON ` + table + ` AS RESTRICTIVE FOR ALL USING (` + pred + `) WITH CHECK (` + pred + `)`,
	}
}

// EnsureRLS puts the wall on every Scoped table. It is a no-op on SQLite. It
// is safe to repeat, and it must be run as the table owner (the migration
// path), never as the application's role.
func (e *Env) EnsureRLS(_ context.Context) error {
	if !e.PG {
		return nil
	}
	return e.doGlobal(true, func(q Querier) error {
		for _, name := range Names(Scoped) {
			for _, stmt := range RLSStatements(name) {
				if _, err := q.Exec(stmt); err != nil {
					return fmt.Errorf("putting row-level security on %s: %w", name, err)
				}
			}
		}
		return nil
	})
}

// RLSState says, for each Scoped table, whether row-level security is on and
// forced and the restrictive policy exists. It reads the catalogs of the
// current schema; empty on SQLite.
func (e *Env) RLSState(_ context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if !e.PG {
		return out, nil
	}
	rows, err := e.DB.Query(`SELECT c.relname, c.relrowsecurity AND c.relforcerowsecurity,
		EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = ? AND NOT p.polpermissive)
		FROM pg_class c WHERE c.relkind = 'r' AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())`, policyWall)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var forced, policy bool
		if err := rows.Scan(&name, &forced, &policy); err != nil {
			return nil, err
		}
		out[name] = forced && policy
	}
	return out, rows.Err()
}

// RoleInfo describes the database role the store is connected as.
type RoleInfo struct {
	Name        string
	Superuser   bool
	BypassRLS   bool
	OwnsTables  bool
	CanSetRole  bool
	Restricted  bool // none of the above: the wall applies to it
	Description string
}

// CheckRole reads what the connected role may do. The wall (row-level
// security) holds only for a role that is not a superuser, has no BYPASSRLS
// and owns none of the Scoped tables; a hosted installation must refuse to
// start otherwise (Stage 7 makes that refusal; Stage 1 reports it).
func (e *Env) CheckRole(_ context.Context) (RoleInfo, error) {
	var r RoleInfo
	if !e.PG {
		r.Description = "SQLite has no row-level security: the wall there is the store and the architecture test"
		return r, nil
	}
	if err := e.DB.QueryRow(`SELECT r.rolname, r.rolsuper, r.rolbypassrls FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&r.Name, &r.Superuser, &r.BypassRLS); err != nil {
		return r, err
	}
	var owned int
	if err := e.DB.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tableowner = current_user AND tablename = 'sessions'`).Scan(&owned); err != nil {
		return r, err
	}
	r.OwnsTables = owned > 0
	r.Restricted = !r.Superuser && !r.BypassRLS && !r.OwnsTables
	var why []string
	if r.Superuser {
		why = append(why, "a superuser")
	}
	if r.BypassRLS {
		why = append(why, "has BYPASSRLS")
	}
	if r.OwnsTables {
		why = append(why, "owns the tables")
	}
	if len(why) > 0 {
		r.Description = r.Name + " " + strings.Join(why, ", ") + ": row-level security does not bind it"
	}
	return r, nil
}
