// Package pulls is pull requests and code review for hosted repositories: a
// change traveling from a source branch to a target branch, with its
// conversation, its reviews, its checks and the rules that say when it may
// land.
//
//   - The record of a pull request lives in the database (tables of schema 33).
//     Its head is also a ref of the repository, refs/pull/N/head, kept in the
//     same ref ledger as branches, so it can be fetched and the commits of a
//     pull request survive the deletion of its branch and a force-push. Every
//     update of the head or the base is recorded as a revision.
//   - Whether a pull request can merge is judged by git (merge-tree
//     --write-tree) in a scratch repository, in the background: a request never
//     waits for it, and a judgment is kept with the head and base it was made
//     for, so a stale one is recognised and redone.
//   - A merge is a server-side write through the ledger's compare-and-swap
//     (storage.Mutate): when a push lands on the target while the merge is
//     being made, the merge is made again on the new tip, never over it.
//
// The package knows nothing about HTTP; internal/api serves it.
package pulls
