# Where artifacts are kept

Artifacts (files a step keeps, and the SBOMs and image provenance kept as artifacts) live in a
**blob store**. There are three:

| Store | Needs | Use it for |
|---|---|---|
| `database` | nothing: the database Launchway already uses | the default on PostgreSQL; any number of servers |
| `filesystem` | a directory (shared by every server, if there are several) | the default on SQLite (one server) |
| `s3` | an S3-compatible bucket | very large or very busy installations |

With **nothing configured**, a server on PostgreSQL keeps artifacts in the database and a server
on SQLite keeps them in `<data>/artifacts`. Several servers sharing a PostgreSQL database therefore
share artifacts with nothing else to run. The Runners page (and `GET /api/v1/artifact-store`)
says which store a server uses, why, and how full it is.

Why SQLite keeps the filesystem: SQLite means one server, whose database file and artifact
directory sit on the same disk, so sharing gains nothing. The filesystem is faster there (measured
below: about 345 MB/s up and 2 GB/s down, against 275 and 750 for the database store on SQLite),
and SQLite has a single writer, so large uploads would queue with every other write.
`artifacts.store: database` works on SQLite too, if you want everything in one file.

## The database store

Content is kept in four tables (schema 17): `blob_uploads`, `blob_parts`, `blob_chunks` and
`blob_objects`.

- **Chunks.** Each uploaded part (8 MiB from runners) is cut into 1 MiB rows of `blob_chunks`,
  each with its own SHA-256. A part is written in one short transaction, so no upload holds a
  transaction open, and completing an upload only writes the object's row: the bytes are never
  copied again. An object becomes visible in the one transaction that completes it, so a server
  that dies mid-upload leaves whole parts or nothing, never half an object; another server carries
  the upload on from where it was.
- **Reading** fetches one chunk per query, so a slow download holds no transaction or snapshot
  open, and checks every chunk's SHA-256 as it is read: a damaged chunk fails the download rather
  than hand out wrong bytes. Ranged reads (resumed downloads, a fetch that broke) start at the
  chunk that holds the offset. The engine also checks each whole file's SHA-256 when it is
  uploaded, as with every store.
- **Several servers.** Two servers storing the same part take turns on the part's row; the second
  replaces the first's chunks. Two completing the same upload take turns on the upload's row; the
  second finds it done. Two uploads of the same key succeed only if the content is the same
  (objects are never overwritten).
- **Garbage.** Deleting an object removes its row at once and its chunks in batches of 16 (16 MiB
  per transaction). Uploads that have had no part for 48 hours (a server that died before it
  recorded them), and the content of aborted uploads, are removed the same way by every server's
  pruner, every minute. Objects under `artifacts/` that no artifact refers to (left when a copy
  crossed a removal) are removed after six hours. Retention is unchanged: artifacts expire and are
  deleted exactly as with the other stores.
- **Limits.** `artifacts.database.max_total_gib` (default 100) bounds everything the store holds,
  uploads in progress included; `artifacts.database.max_blob_mib` (default 2048) bounds one file.
  A step whose artifact would not fit is told before it sends anything ("the database artifact
  store is full: it holds … of its limit of …"), and the step's log says so; the step's outcome
  does not change. The limit is checked against the store's total, refreshed at least every five
  seconds, so several servers writing at once may pass it by what they write in that time.

### Measured

PostgreSQL 17 in a container on a laptop (20 cores, NVMe), the store alone, random (incompressible)
content in the runners' 8 MiB parts, `go test -run DatabaseMeasure ./internal/blobstore/` with
`LAUNCHWAY_TEST_MEASURE=1`:

| Object | Upload | Download | Tables grew by |
|---|---|---|---|
| 1 MiB | 60–70 MB/s (15 ms) | 250–310 MB/s | 1.16× (a 1 MiB object is mostly overhead) |
| 100 MiB | 115–150 MB/s | 410–435 MB/s | 1.04× |
| 1 GiB | 115–135 MB/s | 360–390 MB/s | 1.04× |

- PostgreSQL wrote 1.2 GiB of WAL for the 1.1 GiB uploaded: every artifact byte is written about
  twice (WAL, then the table), and reaches every replica and WAL archive.
- Through the whole system (two server processes, a runner, HTTP), a 320 MB artifact downloaded
  through the server that did not receive it at about 300 MB/s, and the upload, after the first
  server was killed part-way, finished through the second within the same run
  (`TestTwoServersShareArtifactsThroughTheDatabase` in `cmd/launchway`).
- For comparison, on the same machine: the database store on SQLite, about 275 MB/s up and
  700–800 MB/s down; the filesystem store, about 345 MB/s up and 2 GB/s down (from the page
  cache; `TestFilesystemMeasure`). S3 depends on the service and the network.

Compressible files (logs, JSON SBOMs, text reports) take less room: PostgreSQL compresses large
values it can (TOAST), and leaves alone those it cannot.

### What operators should know

- **Space after deleting.** Deleted chunks are dead rows until autovacuum clears them; the space is
  then reused by new uploads but the table files do not shrink. The 1.1 GiB measured above was
  still 1.1 GiB on disk after everything was deleted. That is normal: steady use stays at about the
  size of what is kept. To give space back to the system, run `VACUUM FULL blob_chunks` (locks the
  table; artifact reads and uploads wait) or `pg_repack`.
- **Autovacuum.** Removal in small batches keeps dead rows spread out so autovacuum keeps up. On a
  busy installation, check that autovacuum runs on `blob_chunks` and its TOAST table
  (`pg_stat_user_tables`), and consider a lower `autovacuum_vacuum_scale_factor` for that table.
- **Backups grow with artifacts.** A base backup, `pg_dump` and the WAL archive all carry every
  kept artifact. With `pg_dump`, use the custom format (`-Fc`), which compresses. If you would
  rather not back artifacts up, `pg_dump --exclude-table-data=blob_chunks` leaves them out; after
  a restore, downloads of files whose content is missing say so (the rows remain) and expire as
  usual.
- **Replicas.** Artifact uploads are replicated like any write: plan replication bandwidth for the
  artifact traffic you expect.

### When to move to S3

The database store is meant to let an installation start, and grow a long way, with nothing but
its database. Move to S3 (or another S3-compatible store) when any of these is true:

- you keep more than about **100 GiB** of artifacts (the default limit), or backups and restores of
  the database take too long because of them;
- you upload more than about **50 GiB a day**: at the rates measured above that is minutes of
  database write time, but it doubles in WAL and goes to every replica and archive;
- you need single files larger than **2 GiB** (raise `max_blob_mib` up to the database's comfort,
  or use S3);
- your database is a managed service that charges for storage or I/O at a premium over object
  storage.

## Moving between stores

`launchway blobs copy` copies every stored artifact file that the database does not yet record in
the target store from a source store, and records it there. Servers may keep running.

```sh
launchway blobs status --config launchway.yaml
launchway blobs copy   --config launchway.yaml --from filesystem --to database
launchway blobs copy   --config launchway.yaml --from database --to s3 --s3 s3.yaml
```

- Each file is read from the source and its SHA-256 and size checked against what the database
  recorded when it was uploaded, before the target's upload is completed; the target's copy is
  then read back and checked again. A file the target already has is checked, never overwritten.
  The store marker is copied too.
- It is resumable: the database records which store holds each file (`artifact_blobs.store`), so
  a copy that stops carries on where it was. Run it again after the servers have switched, for
  what servers still on the old store kept meanwhile.
- Files still being uploaded are not copied (it says how many: run it again later); files removed
  while they are copied (their retention ended) are not recorded, and their copy is removed.
- The source is left as it is unless `--delete-source` is given, which deletes each file once the
  target has it. Use it only once no server uses the source.
- `--dry-run` lists what would be copied. `--json` prints the report as JSON. It exits with status
  1 if any file failed.

To move, with servers running:

1. `launchway blobs copy --from OLD --to NEW`
2. Set `artifacts.store` to `NEW` (on PostgreSQL, unset means `database`), and roll the servers.
3. `launchway blobs copy --from OLD --to NEW` again.
4. Optionally, once no server uses `OLD`, the same with `--delete-source` (or remove the old
   directory or bucket prefix).

### Should a server read the old store when the new one lacks a file?

No. A server reads only its own store. Reading the previous store as a fallback was considered and
left out: the previous store's settings and credentials would have to stay in every server's
configuration for good, a filesystem fallback would work on one server and not another (each has
its own directory), and removal would have to reach both stores. Instead nothing is silently lost:

- A file the database records in another store is never forgotten. Its download fails with a
  message that says which store has it and the exact `launchway blobs copy` command.
- At start, the server logs what is kept in other stores; the Runners page shows it with the
  command; `launchway blobs status` lists it.
- New uploads never reuse content this server cannot read: a file kept again is sent again.

## Upgrading to schema 17, and rolling back

On PostgreSQL with nothing configured, the upgrade moves artifacts to the database by itself:

- **New installations, and Helm installations** (whose data directory is an `emptyDir`, so nothing
  kept on the filesystem survives a pod's restart anyway): the servers use the database at once.
  Older artifacts are listed as kept elsewhere until they expire; with an `emptyDir` their content
  is gone already.
- **A single server with a persistent data directory that holds artifacts**: it keeps using the
  filesystem, and says so at start and on the Runners page, until you copy them
  (`launchway blobs copy --from filesystem --to database`); then it moves to the database by
  itself.
- **Several servers that each had a directory of their own** (where only the first kept
  artifacts): all of them use the database; copy the old files from the first server's directory
  with `launchway blobs copy --from filesystem --to database --from-dir …` on that machine.

During a rolling upgrade, older servers next to newer ones: an older server does not know the
database store. With nothing configured it uses its own directory, which lacks the store marker
(now in the database), so it keeps no artifacts and says why; runs that keep or fetch artifacts,
created by a server using the database store, need schema 17 (`"requires": 17` in their pipeline,
`min_schema` 17), so older servers list and show them but leave them to the newer servers. Older
servers do run every other run.

Rolling back to a release before schema 17:

1. Take `artifacts.store: database` and `artifacts.database` out of the configuration (older
   servers refuse settings they do not know). With the Helm chart, unset `config.artifacts.store`
   and `config.artifacts.database`.
2. To keep artifacts on the older release, first copy them into a store it knows and switch to it:
   `launchway blobs copy --from database --to s3` and `artifacts.store: s3` (or a shared
   directory). Otherwise the older servers keep no artifacts until you upgrade again, and the
   artifacts already in the database wait there, untouched.
3. Roll back. Schema 17 is additive, so the older release runs against the migrated database; it
   ignores the new tables. Runs that need schema 17 and were still open wait for a schema-17
   server.
