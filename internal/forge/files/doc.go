// Package files reads and writes what a hosted repository holds, without a
// working copy: a branch's commit log, the entries of a directory, one file's
// text, and one commit that adds or changes one file. Reads run git in the
// server's cache of the repository; a write is a server-side mutation (the same
// path a merge takes), so it ends in the ledger's one transaction and a
// concurrent push is never lost.
//
// Permissions and branch rules are the caller's to check; this package only
// refuses what cannot be done (a name that is not a safe path, a file over
// the limit, a branch that does not exist).
package files
