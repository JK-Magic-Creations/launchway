// Package graph is Launchway's integrated software graph: one graph from code
// to what is built from it to what it needs and where it runs.
//
// It lives in the database Launchway already has (SQLite or PostgreSQL) and
// nothing else: nodes and edges are rows, traversals are recursive queries
// with limits, and every read is filtered by what the reader may read.
//
// Facts come in through small adapters called sources (source.go). A source
// reads one scope (a repository, a project, the installation) and returns the
// complete set of nodes and edges it asserts for it; the store compares that
// with what it held before, so an unchanged fact costs nothing, a changed
// fact closes the old edge and opens a new one, and a fact that is gone is
// closed, never deleted. That makes every answer repeatable "as of" a time or
// a commit, and makes a rebuild from scratch give the same graph as the
// incremental updates (Rebuild).
package graph
