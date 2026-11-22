# Every table has an owner; every query has a scope

Launchway is being built so that several customers, each in its own **Base**, can share one
installation without being able to see, infer or affect each other. This page is the rule that
makes that cheap to keep and impossible to forget, and what to do when you add a table, a query
or a background job. It is a contributor page; nothing here changes how an installation with one
Base behaves, and every installation today has exactly one.

**Where it stands.** In progress. Stage 1 is built: the identity area (accounts, memberships,
sessions, API tokens, one-time links, recovery codes) lives in `internal/store` and is walled; the
rest is registered as pending and moves stage by stage. The server refuses to start with
`multi_base: true` until requests know their Base (Stage 8). Nothing walled is served yet.

## The words

- **Installation**: one running deployment, with its servers, database and root key.
- **Base**: a customer's top level inside an installation; the highest thing that customer can
  see or name. Every installation has the **default Base** (`bs_default`), and until the switch is
  on it is the only one. The settings level that is stored as `installation` is that Base's top
  level.
- **Wing, Squadron, Project**: the levels inside a Base, as before. A Wing is what the code calls
  an `org`.
- **Scope**: a value (`store.Scope`) that says which Base an operation is for.

## The rule

1. **Every table has an owner.** It is global (the installation's own: the schema marker, the
   list of Bases, the keyring's root-wrapped keys), or it is a Base table (it carries `base_id`,
   and every key begins with it), or it is pending (it will be a Base table in a named stage).
   The list is `store.Tables` in `internal/store/tables.go`, and a test fails if a table exists
   that is not in it.
2. **Every query has a scope.** SQL for a Base table is written in `internal/store`, in a method
   whose first parameter after the context is a `store.Scope`, and the statement says `base_id`.
   A test fails if a statement in the store names a Base table without `base_id`.
3. **No new SQL outside the store.** The files that held SQL before the wall are on a baseline
   (`quality/wall-baseline.json`) with the number of statements in each. A file may not gain
   statements and a new file may not hold any; the numbers only fall (`make wall-update` lowers
   them after you move code).
4. **A scope comes from somewhere trustworthy.** A `store.Scope` cannot be written as a literal.
   The only ways to get one are the doors of `store.Registry`: `ScopeForHost` and
   `ScopeForHandle` (the router, from the address a request came to), `ServiceScope` and
   `ForEachBase` (a background job), and `DefaultScope` (the single-Base code that predates all
   this). A test lists the files that may call each door.
5. **The database is the second lock.** On PostgreSQL, with several Bases on, every Base table has
   row-level security on `base_id`, and every scoped call is one transaction that begins by
   setting `app.base_id` for that transaction only. A query that forgot its predicate finds
   nothing instead of everything. The application connects as a role that owns no tables and has
   no `BYPASSRLS`; the owner role is for migrations.

## Adding a table

1. Write the migration as usual (a new number; additive; see [migrations](migrations.md)).
2. Add a line to `store.Tables`. Decide:
   - **Global** only if the row belongs to the installation and no Base should ever read it. Say
     why in the `Why` field.
   - **Scoped** if it holds anything a customer created, owns or could infer. Give it a
     `base_id TEXT NOT NULL` column, begin its primary key and every unique index with `base_id`,
     and begin every index that serves a lookup with it. Names that were unique across the
     installation (a project name, a token's hash, a repository path) are unique per Base now.
   - **Pending** only when you cannot do the above in this change. Name the stage (S2 to S6) and
     say what the rows are keyed by today. Every pending table is a promise; the list is checked
     and only shrinks.
3. Put the SQL in a method of a store type in `internal/store`, taking a `store.Scope`.
   `Env.Do(ctx, scope, write, func(q *Q) error { ... })` gives the statement a transaction with the
   Base set; bind `q.Base()` to `base_id`.
4. Add two Bases to your test and ask for one's things from the other. The isolation tests in
   `internal/engine/wall_test.go` are the pattern: the same names in both Bases, a third Base that
   is empty as the control, and the same answer from "belongs to another Base" as from "exists
   nowhere" (a not-found with the same words).
5. Run `go test ./internal/engine ./internal/store ./internal/archtest` on SQLite and with
   `LAUNCHWAY_TEST_POSTGRES` set.

## Adding a query to something already walled

Use the existing store type and pass the scope you were given. Do not call `DefaultScope` to make
a compile error go away: the request or job you are in already has a scope, or it should (a
background job iterates with `ForEachBase`, which gives each Base its own scope and its own
error).

## What a Base must not be able to learn

Not only rows. A Base must not learn that another exists, how many there are, what they are
called, whether a name is taken, how busy they are, or how long a lookup took because of them:

- A thing in another Base and a thing that exists nowhere give the **same error** (`ErrNotFound`,
  same words).
- A name that is unique **within** a Base (a user, a project, a token's name) is free in every
  other Base. The only global names are Base labels (host names), and a label that is taken,
  reserved or malformed is refused with the same message.
- A router answers an unknown host and a suspended Base identically.
- Ids that a Base sees are opaque and random. A raw auto-increment value (an event sequence, an
  audit id) is never shown across Bases.
- Nothing is deduplicated across Bases (identical files are stored twice), because existence is
  information.

## Turning several Bases on

`multi_base: true` in the bootstrap file (the default is `false`). It does two things at start:

1. It makes the keys of the identity tables begin with `base_id`, once, in one transaction (the
   migration only added the column beside the old keys, so that upgrading never takes away the
   ability to go back to the previous build). This cannot be undone except from a backup, and it
   raises the lowest schema a build may run against to 70.
2. On PostgreSQL it puts row-level security on every Base table. For the second lock to bind, the
   server must connect as a role that owns no tables and has no `BYPASSRLS`; the server logs a
   warning when it does not.

With the switch on, `launchway bases create LABEL` makes a second Base, and `launchway bases list`,
`suspend` and `resume` manage them. The server itself refuses to start while the switch is on, so
nothing can answer a request from the wrong Base.

## What is still ahead

The stages and their sizes are in the design notes; in short, the run engine and its tables (S2),
Git hosting and pull requests (S3), settings, variables, secrets and keys (S4), runners, caches,
blobs and quotas (S5), events, webhooks, notifications and audit (S6), row-level security
everywhere with the full isolation suite as a release gate (S7), creating, exporting and deleting
a Base with host-name routing (S8), the vendor console (S9) and sign-up (S10). Each leaves a
single-Base installation unchanged, and each is finished when its files are off the baseline and
its tables are Scoped.
