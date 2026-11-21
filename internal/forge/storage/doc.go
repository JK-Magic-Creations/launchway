// Package storage keeps hosted Git repositories in the database and the blob
// store, so that any server can serve any repository, an acknowledged push
// survives the loss of a server, and one backup covers everything. It is the
// "ledger, packs and cache" design of doc 26 section 5.4, as the storage spike
// (docs/development/forge-storage-spike.md) measured and attacked it.
//
//   - The database holds the refs of each repository and a manifest of its
//     packs: immutable packfiles named by their SHA-256, which live in the blob
//     store. A per-repository generation number counts every change.
//   - Each server keeps a disposable cache: a bare repository per hosted
//     repository, checked against the generation before it is used and rebuilt
//     from the manifest when it is behind, missing or damaged. Nothing on a
//     server's disk matters.
//   - A push is received by the real git binary (receive-pack, through
//     internal/gitexec). Git's hooks, which are symlinks to the launchway
//     binary, talk to the server over a Unix socket: pre-receive uploads the
//     pack git holds in quarantine and registers it as pending; the
//     reference-transaction hook, in its prepared state, is the commit point:
//     one database transaction compares and swaps every ref, makes the pack
//     live and bumps the generation. A push whose refs are not where the pusher
//     said they were changes nothing. A push is never acknowledged before that
//     transaction commits.
//   - Maintenance consolidates packs under a lease and retires the old ones;
//     retired blobs are deleted only after a grace period, and a server that
//     finds a pack gone reads the manifest again.
//
// The package knows nothing about HTTP or SSH: Receive and Upload take the
// request and response streams of git's stateless protocol.
package storage
