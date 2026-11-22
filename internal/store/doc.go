// Package store is the one place that writes SQL for data that belongs to a
// Base, and the wall between Bases (docs/development/the-wall.md).
//
// Three locks make a Base unable to see, infer or affect another:
//
//  1. The type system. Every method that touches a Base's data takes a
//     Scope as its first argument. A Scope cannot be written as a literal
//     outside this package; it is issued by a Registry, through four doors
//     that the architecture test limits to named files (DefaultScope,
//     ScopeForHost, ScopeForHandle, ServiceScope and ForEachBase).
//  2. The source. A test refuses SQL text outside this package except in
//     files on a legacy allow-list that can only shrink, and refuses any
//     statement in this package that names a Base table without base_id.
//  3. The database. On PostgreSQL every Base table can carry a restrictive
//     row-level security policy on base_id, and every scoped transaction
//     sets app.base_id for itself only (SET LOCAL semantics), so a query that
//     forgot its predicate finds nothing instead of everything.
//
// Tables are listed once, in Tables (tables.go). A table that exists in the
// schema and not in that list fails a test, which is how a new table, from any
// branch, cannot be forgotten.
package store
