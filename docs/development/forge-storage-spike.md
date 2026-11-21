# Repository storage spike: results and recommendation

Story S34-08. Two candidate designs for hosting Git repositories on several servers were built in their
smallest honest form (`internal/forge/storagespike`, tests only, not wired into the server) and attacked
with the same failure injection and the same properties, using the real `git` binary, real hooks, real
smart HTTP is not needed (the spike pushes through git's own transport). The code that follows from the
verdict is `internal/forge/storage` (S34-09, see [forge-storage.md](forge-storage.md)).

## The two candidates

**A, "volume"** (Gitea-style). One bare repository per repository on a persistent volume, written only by
`git receive-pack` with git's own atomic ref transactions. The database gets a ledger of ref updates,
written from `post-receive`, for events and audit. Off-node copies come from mirroring (a push mirror to
another device or host). HA would be added later by replicating the bare repositories.

**B, "ledger"** (doc 26 section 5.4). Refs and a manifest of immutable packs in the database, the packs in
the blob store, a disposable per-server cache checked against a generation number. The commit point of a
push is a database compare-and-swap inside git's `reference-transaction` hook.

## What Gitea and Gitaly actually do

Read for this study: Gitea's smart-HTTP handler (`routers/web/repo/githttp.go`) and hook commands
(`cmd/hook.go`), its backup documentation, and GitLab's Gitaly Cluster documentation. Gitea is MIT; nothing
was copied, and Forgejo was not read.

- **Gitea stores repositories as bare repositories on a filesystem** and runs the real git:
  `git upload-pack` and `git receive-pack` with `--stateless-rpc`, `--advertise-refs` for the first call,
  gzip request bodies, `GIT_PROTOCOL` from the `Git-Protocol` header (validated by a regular expression).
  It takes **no repository lock of its own** around a push: git's per-ref lock files are the only
  serialisation. Its hooks (`pre-receive`, `update`, `post-receive`, `proc-receive`) are subcommands of its
  own binary that call back into the server (`private.HookPreReceive` and friends) with the push's context
  in environment variables set by its `serv` command; a hook that finds no Gitea environment refuses the push.
  This is the same shape as the hooks built here (symlinks to one binary, a socket back to the server).
- **Gitea's backup is `gitea dump`**: a zip of the config, data directory, a complete copy of `repos/` and an
  SQL dump, and its documentation says the instance "must be shut down during backup". Restore is manual and
  ends with `gitea admin regenerate hooks`. There is no consistent online backup of repositories and database
  together; this is the gap HG-24 of document 32 names ("the restore drill proves the database, not the
  repositories").
- **Gitea scaling and gc**: its documentation page on running several instances could not be fetched (404), so
  what it needs shared (the repository directory, with the database, cache and queue) is from memory and
  unverified; garbage collection is git's own `git gc`, run by a periodic job. Both are stated as such.
- **GitLab's Gitaly Cluster (Praefect)** keeps bare repositories on several Gitaly nodes with a Praefect
  routing layer and PostgreSQL for metadata. Writes go synchronously to every healthy, up-to-date replica,
  outdated ones are repaired asynchronously; failover needs ten consecutive failed one-second health checks;
  its own documentation lists, among limitations, that snapshot backups are unsupported (use its backup
  tasks), that a race can make CI pipelines hit a replica that has not received a write yet, and that
  Kubernetes support is beta and not for production. It is a separate component to run, which is what
  "one binary and a database" rules out.

## Method

Everything runs real git (receive-pack, upload-pack, index-pack, pack-objects) through `internal/gitexec`,
with the hooks installed as symlinks to the test binary. Failures are injected inside those real processes,
by name, through the environment: `kill -9` of the whole push at each of eleven points (before the upload,
after the upload, after registering the pack, with git's locks held, between the statements of the commit
transaction, after the commit, after git moved its ref, after `post-receive`), the database connection
dropping before and after the commit, the blob store down, slow, failing a part, storing a pack cut short or
with one byte changed, a server killed during its sync, a pack retired and deleted while a server downloads
it, `git gc` run continuously while pushes are in flight, two servers pushing one ref at once, a wiped cache,
the loss of the node, and backup and restore. After every case the properties are checked from a cold
server that holds nothing: no acknowledged push is lost (its ref exists and holds the commit pushed or a
descendant, with its whole history fetchable), no ref points at missing objects (`git fsck --full --strict`
of a fresh clone), the refs a cold server serves are exactly the refs of record. "Acknowledged" means the
client's `git push` exited 0.
