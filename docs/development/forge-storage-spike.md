# Repository storage spike: results and recommendation

Story S34-08. Two candidate designs for hosting Git repositories on several servers were built in their
smallest honest form (`internal/forge/storagespike`, tests only, not wired into the server) and attacked
with the same failure injection and the same properties, using the real `git` binary, real hooks, real
smart HTTP is not needed (the spike pushes through git's own transport). The code that follows from the
verdict is `internal/forge/storage` (S34-09, see [forge-storage.md](forge-storage.md)).

## The two candidates

**A, "volume"** (plain bare repositories). One bare repository per repository on a persistent volume, written only by
`git receive-pack` with git's own atomic ref transactions. The database gets a ledger of ref updates,
written from `post-receive`, for events and audit. Off-node copies come from mirroring (a push mirror to
another device or host). HA would be added later by replicating the bare repositories.

**B, "ledger"** (doc 26 section 5.4). Refs and a manifest of immutable packs in the database, the packs in
the blob store, a disposable per-server cache checked against a generation number. The commit point of a
push is a database compare-and-swap inside git's `reference-transaction` hook.

## What is common practice

The usual way to host Git is bare repositories on a filesystem, serving them with the real git
(`git upload-pack` and `git receive-pack` with `--stateless-rpc`, `--advertise-refs` for the first call,
gzip request bodies, `GIT_PROTOCOL` from the `Git-Protocol` header). Nothing takes a repository lock of
its own around a push: git's per-ref lock files are the only serialization. Hooks (`pre-receive`,
`update`, `post-receive`) are subcommands of the server's own binary that call back into the server with
the push's context in environment variables. This is the same shape as the hooks built here (symlinks to
one binary, a socket back to the server).

The weaknesses of that way are known too: backup is a copy of the repository directory taken with the
server shut down, with no consistent online backup of repositories and database together (this is the gap
HG-24 of document 32 names, "the restore drill proves the database, not the repositories"); garbage
collection is git's own `git gc`, run by a periodic job; and a design that spreads repositories over
several storage nodes needs a routing layer and a metadata database of its own, with failover and
replication limits, which is a separate component to run, and "one binary and a database" rules that out.

## Method

Everything runs real git (receive-pack, upload-pack, index-pack, pack-objects) through `internal/gitexec`,
with the hooks installed as symlinks to the test binary. Failures are injected inside those real processes,
by name, through the Airfield (environment): `kill -9` of the whole push at each of eleven points (before the upload,
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

## Results

Hardware and versions: an Intel laptop with 20 threads, 31 GB RAM, NVMe, Linux 7.3, git 2.56.1 (the hook
order and the one-pack-per-push behavior were checked on git 2.28.0 too, built in a container), Go 1.27.4,
SQLite through modernc, PostgreSQL 17 in a container for the concurrency run. Timings were taken on a quiet
machine (load about 3) with the temporary directory on the NVMe. The large repository is **200 MB** of
incompressible data (twenty 10 MB files in one commit), not the 500 MB of the brief: an earlier 500 MB run
on a loaded machine (load 20) took 266 s to push on candidate A and was discarded as noise; the owner's
whole reference installation is 45 MB.

### Properties

| Property | A volume | B ledger |
|---|---|---|
| kill -9 at each of 11 steps of a push; the retry succeeds, nothing acknowledged is lost, no ref points at missing objects | pass (after a fix, see "found") | pass |
| the database drops the connection before and after the commit | pass (the ledger is not on the commit path) | pass; "after" commits and reports failure, a retry says up to date |
| two servers push one ref at once (SQLite 12 rounds each; PostgreSQL 100 rounds, B): exactly one wins, the other is refused as stale | pass (git's own ref lock) | pass |
| a fetch on another server right after a push sees it | pass (one volume) | pass |
| cache wiped: fetch is correct | not applicable: the volume is the data | pass, content byte-identical |
| node lost | **loses every acknowledged push since the last mirror run** (2 of 8 with a mirror every third push) | pass: nothing is lost, all state is in the database and the blob store |
| gc / repack never deletes reachable objects | pass: `git gc` run 34 times while 15 pushes landed | pass: consolidation, collection with no grace period, cold fetch identical; a server caught mid-sync when its pack is retired and deleted reads the manifest again |
| backup and restore are byte-identical | pass (a copy of a quiet volume) | pass (one consistent database copy, plus the blob directory, or the database alone with the database store) |
| the blob store is down, slow, fails a part, stores a pack cut short | n/a | pass: the push fails before the commit, nothing moves; warm servers keep serving reads, a cold one refuses |
| the blob store stores a pack with one byte wrong, same size, and says yes | n/a | **fails without read-back verification**: the push is acknowledged and a cold server cannot be built (shown on purpose); passes with it |

The injection campaign (every point above chosen at random, retries on either server, caches wiped and
repacks mixed in) ran as two Sorties (runs) of 600 rounds on candidate B with the filesystem blob store (seeds 1 and 2): 1,070 of the
1,200 rounds had a failure injected, 1,200 pushes were acknowledged in the end after retries, and every
property held at the end and every 40 rounds, with no acknowledged push lost. Candidate A, 600 rounds
(543 injected): every property held too (the node-loss case is separate, above). The database blob store
shares the code and ran the smaller campaigns of the test suite; the PostgreSQL run is the 100-round race
only (run before the instruction to run no more PostgreSQL suites).

### Latency and storage (200 MB large repository, SQLite, quiet machine)

| | A volume | A, `core.fsync=all` | B ledger, filesystem store | B, filesystem, read-back | B, database store |
|---|---|---|---|---|---|
| small push p50 / p95 (4 KB, 40 pushes) | 31 / 34 ms | 34 / 37 ms | 48 / 51 ms | 48 / 52 ms | 45 / 52 ms |
| small clone p50 | 27 ms | 27 ms | 24 ms | 24 ms | 24 ms |
| small clone on a server with no cache | n/a | n/a | 166 ms | 163 ms | 172 ms |
| push of 200 MB | 20.0 s | 19.9 s | 17.6 s | 16.9 s | 17.2 s |
| clone of 200 MB, warm | 6.4 s | 6.2 s | 2.3 s | 2.4 s | 2.6 s |
| clone of 200 MB, cache lost | n/a | n/a | 2.3 s | 2.3 s | 2.6 s |
| 4 KB push on top of the large repository | 31 ms | 33 ms | 47 ms | 43 ms | 43 ms |
| clone on a server one push behind | 6.3 s | 5.9 s | 1.2 s | 1.2 s | 1.3 s |
| stored (data 210 MB) | 210 MB | 210 MB | 210 MB blobs + 210 MB cache | same | 210 MB in the database + 210 MB cache |

Reading the table: B costs about 15 ms more per small push than A (47 vs 32 ms p50): one extra process
for the wrapper's sync check, an upload, a read-back, and a database transaction. Both are far inside the
300 ms target. B fetches the large repository faster than A (2.3 s against 6.4 s) because it keeps the push as
one pack, where A's git (default `receive.unpackLimit` of 100) explodes it into loose objects that every
clone must repack; A could be given the same setting. A cold sync of 200 MB, a server that has lost its
cache, takes 2.3 s (a cold sync of the small repository, 0.17 s); extrapolated, 500 MB is about 6 s, well
under the minute target (not measured at 500 MB, see above). A tiny push on top of the large repository takes
the same time as on a small one, and a second server that is one push behind fetches only the new pack.
Storage: B holds the data twice on one machine (blob store and cache) and once more in each other server's
cache: 210 MB stored for 210 MB of data, plus a 210 MB cache per server. The database store costs 210 MB in
the database for 210 MB of data; the write-ahead log doubling that doc 26 measured on PostgreSQL (1.2 GiB of
log for 1.1 GiB uploaded) applies to it and is not reproduced on SQLite, where the log is written to a checkpoint.

### What the spike found, besides the comparison

1. **A git killed with -9 leaves lock files** (`refs/heads/<name>.lock`) and a retry is refused for ever:
   this was the first failure of both candidates. The fix is the guard of `internal/forge/storage`: every
   receive-pack holds the cache's flock shared; whoever can take it exclusively knows none is running, and
   removes the locks. Any server that takes no repository lock of its own has the same exposure; an operator
   cleans the files by hand.
2. **A pack read back is the only defense against an object store that lies.** Size checks catch a pack cut
   short; one changed byte is caught only by hashing what was stored. Read-back is on by default in
   S34-09 (it costs nothing measurable here, the file was in the page cache; for S3 it is a second transfer,
   unless the store checks content on write).
3. **The commit point works as designed.** With `receive.unpackLimit=1` every push, thin ones included,
   arrives in git's quarantine as exactly one pack, and git moves the pack into the repository before the
   `reference-transaction` hook runs in its `prepared` state, on git 2.28.0 and 2.56.1 alike. A hook that
   refuses `prepared` leaves the ref where it was; the objects stay in the cache as unreachable garbage
   (harmless, never uploaded to the manifest as live). Concurrent pushes to *different* refs of one
   repository both land.
4. **The client's message.** `reference-transaction` stderr does not reach the client; a stale loser sees
   git's generic "failed to update ref" (and on 2.28 "the remote end hung up"). The server logs the reason
   and `Receive` returns it. A friendlier message needs the compare-and-swap in `update` or `pre-receive`,
   which is a later refinement.
5. **Coverage-instrumented hooks stall the push.** A Go test binary built with `-cover` and started by git
   as a hook hangs, and with it the client; the permanent suite points the hooks at the real binary under
   `-cover`. Nothing in production is instrumented.

## What plain storage does and does not give

Plain storage is proven for one node, has no moving parts beyond git, and is the fastest to a working
push. It meets every property except two, and both matter here: a lost
node or volume (a single-server installation keeps its repositories on one disk) loses what
the last mirror run had not copied, and there is no restore drill that covers the repositories and the
database together (document 32, HG-24, is exactly this gap). Adding a synchronous off-node copy to A means
uploading the pack to a second store before acknowledging, which is B's pre-receive step; the design then
has two sources of truth. Replicating bare repositories needs a component to run, and its
own documentation lists snapshot backups as unsupported and a stale-replica race.

B is better on every durability property, needs only the database and a blob store a small installation already
backs up and drills, and with the database store the one nightly database dump holds the repositories:
the owner's 44 repositories are 45 MB. Its costs are code (the hook socket, the manifest, maintenance), about
15 ms per push, and two limits stated below. The provider interface does not depend on the choice: storage
is behind `internal/forge/storage`, and a volume implementation can be added behind the same calls.

## Recommendation for Milestone 1

**Commit to B ("ledger, packs and cache"), with the database as the blob store on a small installation and
read-back verification on, and build it as `internal/forge/storage` (done: S34-09).** The coordinator's
expectation, that A is simpler and is fastest to a working server on one node, is confirmed as to effort and
refuted as to the requirement the owner set first: no acknowledged push is ever lost. A fails it on the loss
of the node, B does not, and the difference is measured, not argued. Keep A's lessons (the lock cleanup,
`git gc` safe by default grace) and keep a volume backend as a later option behind the same interface for
installations that want one.

Where B stops being a good idea: with the database as the store every byte is written about twice to
PostgreSQL and reaches every standby, and one pack is at most `artifacts.database.max_blob_mib` (2048 MiB
by default) with the store capped by `max_total_gib` (100 by default); the usage report warns at 80% of
each, and past them an S3 or filesystem store is the answer (two things to back up, joined by the store
marker). A repository over a few GiB, or a push over 2 GiB, is not for the database store.

## Failure matrix of doc 26 section 5.4

| Failure | Outcome (test) |
|---|---|
| a server dies mid-push, before the commit | nothing visible; the pending pack is collected after a day (`TestKillTheServerAtEveryStepOfAPush`, `...uploads of pushes that never committed are collected`) |
| a server dies after the commit, before git moves its ref | the push is committed, the client sees failure, the retry says up to date; the cache catches up on the next request |
| two pushes to one branch | one wins, one refused as stale, 100 rounds on PostgreSQL |
| blob store unavailable | pushes fail before the commit; warm caches serve; cold servers refuse |
| database unavailable or connection dropped | pushes fail; the "after the commit" drop is committed and reported as failed; reads of a current cache keep working only in the in-process test, as `serve_stale` is not built (off by default per the design) |
| a cache is damaged | `Verify` runs `git fsck` and rebuilds it |
| a pack in the blob store is damaged | refused on sync, the repository is flagged, maintenance will not repack it; found by `Verify` before anyone fetches |
| git killed with -9, lock files left | removed by the next push (the guard) |
| a client waits on a server that stopped answering | bounded by the client's deadlines (the tests assert it); see S34-12 |
