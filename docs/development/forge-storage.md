# Hosted repository storage

How Launchway keeps the Git repositories it hosts (`internal/forge/storage`). The design and the
evidence for it are in [forge-storage-spike.md](forge-storage-spike.md); this page is what an operator
and a developer need to know about the code that was built from it.

## What is kept where

| What | Where | Notes |
|---|---|---|
| Refs of every repository | database, `repo_refs` | the refs of record: every server serves exactly these |
| A generation number per repository | database, `repo_generation` | bumped by every change; caches are checked against it |
| A manifest of packs | database, `repo_packs` | `pending`, `live`, `retired`, `orphan`; one row per immutable pack |
| The packs | the blob store (database, filesystem or S3) | named by SHA-256, never shared between repositories |
| A cache of each repository | each server's cache directory | a bare repository; disposable |
| The store marker | `meta.repo_store_marker` and an object in the blob store | a server whose blob store lacks the object refuses to host |

Back up the database and you have the repositories, when the blob store is the database. With a
filesystem or S3 store there are two things to back up, and the marker says whether they belong together.

## A push

1. `git receive-pack` receives the push into git's quarantine as one pack (`receive.unpackLimit=1`,
   `receive.fsckObjects=true`).
2. `pre-receive` (the launchway binary, started as a symlink named after the hook) uploads that pack to
   the blob store, reads it back to check its SHA-256, and registers it as `pending`. A pack over
   `MaxPackBytes`, or a repository that would pass `MaxRepoBytes`, is refused here with a message.
3. `reference-transaction prepared` is the commit point: one database transaction compares and swaps
   every ref, makes the pack `live` and bumps the generation. A ref that is not where the pusher said
   it was changes nothing and refuses the push.
4. Git moves its own refs. The push is acknowledged after that, never before step 3.

A push that dies before step 3 changes nothing visible; its pending pack is collected after a day. A
push that dies after step 3 is committed even if the client was not told, and a retry says "up to date".

## Reads

A fetch asks the database for the repository's generation (one indexed read). If the server's cache is at
it, the cache serves. If not, the server downloads the packs it lacks, indexes them, and only then writes
the refs. A pack that was retired while it was being downloaded makes the server read the manifest again.

## What `dataVolume` means now

The server's cache directory is a cache, sized to the working set, not to every repository. Losing it,
or the whole pod, loses nothing; the next request rebuilds what it needs. A persistent volume for it
only makes a restart warm sooner. (The chart change is story S34-47.)

## Limits

With the database as the blob store every byte is written about twice to PostgreSQL (the log and the table)
and reaches every standby, and one pack is at most `artifacts.database.max_blob_mib` (default 2048). The
usage report (`Store.Usage`) warns at 80% of a repository cap, of the largest object and of the store's
capacity; past that, use the S3 or filesystem store.

## Maintenance and checks

- `Maintain` consolidates when more than 8 packs are live, under a lease, and retires the old packs;
  `Collect` deletes retired blobs after 24 hours and pending ones after 24 hours.
- `Verify` re-reads packs against their SHA-256 and runs `git fsck` on this server's cache, which it
  rebuilds when damaged. A pack that does not match flags the repository; a flagged repository is not
  repacked.
- `EvictCaches` removes least-recently-used caches, never one in use.
- Stale `*.lock` files left by a git that was killed are removed before the next push, safely: a running
  receive-pack holds the cache's lock shared, so when it can be taken exclusively, none is running.

## Git version

`reference-transaction` needs git 2.28. The sequence of hooks and the one-pack-per-push behaviour were
checked on git 2.28.0 and 2.56.1. The server logs the installed version at start-up and warns about what
it is too old for.
