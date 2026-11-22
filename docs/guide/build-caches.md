# Build caches

Keep dependencies between runs, so that a step does not start from nothing every time.

A step can name directories to restore before its script runs and to save after it, under a key
made from a lock file. The first Sortie (run) that sees a lock file installs everything and saves it;
every later run with the same lock file restores it in seconds. Nothing but the database
Launchway already needs is required: entries are kept in the same store as Cargo (artifacts), which is the
database unless you chose a filesystem or S3 ([where Cargo are kept](../artifacts.md)).

```yaml
steps:
  test:
    image: docker.io/library/python:3.13-slim
    cache:
      - name: pip
        paths: [.cache/pip]
        key: { files: [poetry.lock], versions: [python --version] }
        fallback: latest      # none (the default) or latest
        max_age: 7d           # rebuild from nothing after this long
        save: success         # success (the default), always or never
    env: { PIP_CACHE_DIR: .cache/pip }
    run: pip install -r requirements.txt && pytest
```

## The rule

**A cache makes a build faster. It never changes what the build produces.** If a build with a warm
cache could give a different answer than the same build with none, the cache is wrong, and the
fix is in the cache, not in the build. Launchway helps in four ways: the key says everything a cache
depends on, an entry that changed is saved again instead of being kept as it was, an entry expires
and is rebuilt from nothing, and what is restored keeps its file times so that tools which compare
them do not take old output for new. The rest is a matter of what you cache, below.

## The key

The key is made of, always: the format of the entry; the Station's operating system and
architecture; the cache's name; and **the step's image resolved to its digest**. A tag such as
`python:3.13` names whatever the Depot (registry) holds today, so the key uses what it resolved to: a
different JDK, Node or Python behind the same tag is a different key and never restores an
incompatible entry. (An image written with `@sha256:` is used as written. A container engine reports its own copy; the Kubernetes
executor asks the image's Depot. If neither can say, the key uses the reference as written and
the log says so; pin the image by digest, or name the tool's version under `key.versions`.)

You add: the paths and SHA-256 contents of the files `key.files` matches, in sorted order; the text of
`key.text`, so that a team can start over by hand by changing it; and `key.versions`, commands run in the step's
image whose output is part of the key (`java -version`, `node --version`), or files whose contents are
(`{ file: .nvmrc }`). A changed lock file is a new key and so a new entry. If `key.files` matches no
file, the cache is not used, and the step's log says so.

If the step changes a file the key is made of (an install that rewrites a lock file), the entry is
saved under the key of the files as the step left them, never under the key from before it; the log
says so.

The Sortie page and `launchway runs show` list everything a key was made of, so that a surprising miss
can be explained: which input changed.

## Generations and maximum age

An entry is never changed once saved. If a step restores an entry and leaves different contents (a
snapshot or a version range resolved to something newer, say), it saves the **next generation** of
the same key; a restore takes the newest generation and the older one ends. If it left the paths
as they were restored, nothing is saved. (The step compares what it left with what the entry holds,
so a large cache costs a pass over its files after the step, even on a hit; `save: never` avoids
it.)

An entry also has an age, counted from when its contents were last **started from scratch**. What a
step saves after restoring something keeps that age, so a chain of refreshes does not live for ever.
Past `max_age` (7 days unless the cache or the Flight's cache policy says otherwise; a cache may ask
for less than the policy, never more) a restore treats the entry as missing, the step rebuilds the
cache from nothing, and that rebuild starts a new chain. The Sortie says `Too old` for such an entry. An
entry restored as a fallback does not keep itself alive by being used.

With `fallback: latest`, a step whose key has no entry starts from the newest entry of the same name
and platform that its run may read (younger than the maximum age), and saves its own result under the
new key. It grows, and keeps what dependencies dropped, until the maximum age ends the chain; set `max_age` when you use
it. The Sortie records the entry actually restored, with its own key, generation and age, not the step's key.

## Who can read and write what

The server, not the Station (runner), decides which entries a run may touch, from how the Sortie started. This
matters because a pull request runs code from anyone who can open one: what it writes must never
be restored by a Sortie that is trusted with secrets or deploys.

| Run | Reads | Writes |
|---|---|---|
| A push, schedule, webhook or manual run on the default branch | `default` | `default` |
| Another branch, or a tag | its own scope, then `default` | its own scope |
| A pull request | `pull:<number>`, then `default` | `pull:<number>` |
| A pull request from a fork | `pull:<number>` only | `pull:<number>` |
| A draft run (unsaved pipeline text) | `default` | nothing |

On top of the scope, every entry belongs to **its pipeline**: two pipelines of a Flight (codebase) that use the
same cache name do not see each other's entries, so a lint pipeline cannot seed the release pipeline.
Caches that name the same `share` group (and the same cache `name` and scope) do share. A cache marked
`sensitive: true` is never read outside the scope that wrote it; an entry saved as sensitive stays
that way even if a pull request's own pipeline says the cache is not.

So a pull request starts from what the default branch built, and saves only where the default
branch never looks. Nothing is shared between Flights. Launchway takes the default branch from the
Flight's repository; if it cannot tell which it is, no Sortie writes `default` (a run of any branch then
saves to its own branch scope) rather than guess. The same scopes decide whose **image cache** an
image step uses: the protected tag only a default-branch run writes, a tag of its own for every other
scope (a pull request imports the protected one and exports only to its own; a fork's reads only its
own).

## What not to cache

A cache is safe when it holds what the tools check themselves: downloaded packages named by a lock
file, content-addressed stores (the Go build cache, ccache, pip's and npm's download caches, Gradle's
`caches/modules-2`). It is unsafe when it holds what depends on time or on the sources that made it.
`launchway validate` and the pipeline editor warn about the common cases (they are warnings, not
errors):

- **Build output** (`target/`, `build/`, `dist/`, `node_modules/`, `__pycache__`, a Gradle build cache): it
  depends on the commit that made it, and tools that decide by file time may keep it. Cache the download cache and let the
  build recreate its output.
- **A whole `.m2` or `.gradle`**: it holds the resolver's record of what it asked for and what was not
  found (`maven-metadata-*.xml`, `*.lastUpdated`), so a snapshot or a version published since can be
  missed. Cache `.m2/repository`, and run Maven with `-U` (Gradle with `--refresh-dependencies`) or delete
  those files before the cache is saved.
- **A key without a lock file** for a dependency cache: a manifest does not say which versions were
  resolved. Put the lock file in `key.files` and install from it (`npm ci`). Maven has no lock file: set a short `max_age`.
- **`fallback: latest` without `max_age`**, and **`save: always`**, which saves what a failed step left.
- **A path where tools leave credentials** (see the next section).

A cache is a place for a tool's own downloads, not for what you want to be fresh. If a thing must be
current (a vulnerability database, a snapshot you depend on), do not cache it, or re-resolve it in
the step. (Launchway's own image scan never uses the build cache for this reason.)

## Credentials are never saved

A cache is restored by every Sortie that may read it, so a credential in one reaches them all. A cache
that holds a file that looks like a credential is **not saved**, from any scope: `.netrc`,
`.git-credentials`, private keys, `.env`, an `.npmrc` with an auth token, a Maven `settings.xml` with a
server password, a `gradle.properties` with a password, a Docker `config.json` with Depot auth, a
kubeconfig with a token, `~/.aws/credentials`, a `.pypirc` or pip configuration with credentials in an
index URL, and the like. The step's log names the file and the fix: narrow the cache's paths so they
leave it out (cache `.m2/repository`, not the whole `.m2`) and use short-lived Depot credentials that are
not written into the workspace. A reference such as `${NPM_TOKEN}` is not a credential. There is no setting that
turns the refusal off.

## What the step's log says

```text
Cache pip: restored key 3f9c… (exact, generation 1, 3 days old, 118 MiB, 4 s)
Cache pip: no entry; the step runs without it
Cache pip: the entry with this key (generation 2) is 8 days old, more than the 7 days this cache may be; it is not restored and the step rebuilds it
Cache pip: the step left the cached paths as they were restored; nothing to save
Cache pip: saved 120 MiB as generation 2 of key 3f9c… (the step changed what was restored)
Cache pip: not saved: it holds 1 file(s) that look like credentials: .m2/settings.xml (a Maven settings.xml with a server password) …
```

The same results are on the step in the Sortie page, in `launchway runs show` (with what the key was made
of), in the Sortie's JSON (`caches` on each step) and in the MCP tool `get_run`. A cache never changes a step's outcome: if it
cannot be restored (a damaged entry, a server that cannot be reached) or saved (over the limits,
caching off, credentials), the step runs on, and the log says why. A damaged entry is ended so that
the next step saves a good one.

## What is kept, and for how long

Regular files and directories are kept, with their permissions reduced to readable and, where it was
executable, executable, and **with their modification times**, to the nanosecond. A restored file gets
the time it had when it was saved, never later than the restore. The checkout is made before the
restore, so its files carry the time of the checkout and everything restored is older than every
source: a tool that compares times (make, Maven's incremental compilation, Cargo, bundlers, a
resolver's "checked less than a day ago") sees restored output as older than the sources, and
rebuilds it instead of taking output built from other sources for current. Symbolic links are left out, and the log says which.
A path that names the repository's `.git` directory (`.git`, `.g*`, `.*`) is refused when the pipeline is
checked, and files inside a `.git` directory are never kept, whatever the paths match.
Entries are tar archives compressed with zstd. By default a Flight keeps 10 GiB of caches, one entry
may hold 2 GiB, an entry nobody restored exactly for 7 days is ended, and so is one whose contents
were started from scratch more than 7 days ago. An entry counts against the quota from the moment its
save begins, so uploads in progress take room too. When a save would pass the quota, the Flight's
least recently used entries are ended to make room, those of pull requests and branches before the
default branch's, or the save is refused if it cannot fit. An administrator changes these in the database, for the installation or for one Flight: the
**Caches** page of the Flight, `launchway caches policy` (`--max-age-days` for the maximum age), `PUT /api/v1/cache-policy` and the MCP tool
`set_cache_policy` do the same thing, and every change is audited. With caching off for a Flight,
lookups miss and nothing is saved, and the pipeline still passes validation.

People with the operator role delete entries (one, or a whole cache by name) from the Flight's
**Caches** page, `launchway caches delete`, `DELETE /api/v1/projects/{name}/caches` and the MCP tool
`delete_cache`; viewers list them (`launchway caches list`, `list_caches`). The next Sortie that wanted a
deleted entry starts without it and saves it again.

## What a cache entry is not (yet)

Two protections are **not** part of build caches today, and neither should be assumed:

- **Entries are not encrypted by Launchway.** The cached archive sits in the blob store as it was
  built (see [encryption at rest](encryption-at-rest.md): build caches are on its list of what is not
  yet covered). `sensitive: true` limits who may *read* an entry through Launchway; it does not
  encrypt it. Do not cache a credential; the build refuses to save a cache whose paths hold one.
- **Entries are not signed.** What stops one run from planting an entry for another is the scope,
  pipeline and `share` rules above, decided by the server; a Station cannot choose them. Signing each
  entry with the Station's identity (so that a stolen blob-store write cannot plant one either) is
  planned, not built.

Both belong to the same next slice as the per-object encryption of caches, so they will arrive together.

## Upgrading from an earlier release

Keys change once: the image digest and the new format are part of every key, so the first run after
the upgrade misses, rebuilds and saves, and the old entries are never restored again; they end under
the idle rule (7 days by default). Entries are also now per pipeline, so a cache that two pipelines
shared by name is rebuilt by each; add `share: <name>` to the caches that should still be shared.
Nothing in a pipeline has to change. A cache that holds a credential file, which was kept with a
warning, is now not saved until its paths are narrowed. The schema change (35) is additive; see
[database migrations](../development/migrations.md).

## Stations and older servers {#runners-and-older-servers}

Stations that know caches say so, and a step with `cache:` is only given to them. A pipeline with a
cache carries `requires: 25`, so servers older than that leave its Sorties alone; use the key only once
every server and Station is upgraded (see [database migrations](../development/migrations.md)).

See the [pipeline reference](../reference/pipeline.md#cache) for every key and limit.
