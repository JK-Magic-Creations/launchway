# Build caches

Keep dependencies between runs, so that a step does not start from nothing every time.

A step can name directories to restore before its script runs and to save after it, under a key
made from a lock file. The first run that sees a lock file installs everything and saves it;
every later run with the same lock file restores it in seconds. Nothing but the database
Launchway already needs is required: entries are kept in the same store as artifacts, which is the
database unless you chose a filesystem or S3 ([where artifacts are kept](../artifacts.md)).

```yaml
steps:
  test:
    image: docker.io/library/python:3.13-slim
    cache:
      - name: pip
        paths: [.cache/pip]
        key: { files: [requirements.txt, requirements-dev.txt], text: v1 }
        fallback: latest      # none (the default) or latest
        save: success         # success (the default), always or never
    env: { PIP_CACHE_DIR: .cache/pip }
    run: pip install -r requirements.txt && pytest
```

## The key

The key is made from the paths and SHA-256 contents of the files `key.files` matches, in sorted
order; the text of `key.text`, mixed in literally, so that a team can start over by hand by changing
it; and the runner's operating system and architecture, which are always part of it. A changed
lock file is a new key and so a new entry. If `key.files` matches no file, the cache is not used,
and the step's log says so.

With `fallback: latest`, a step whose key has no entry starts from the newest entry of the same
name and platform that its run may read, and saves its own result under the new key; the install
then only has to add what changed.

## Who can read and write what

The server, not the runner, decides which entries a run may touch, from how the run started. This
matters because a pull request runs code from anyone who can open one: what it writes must never
be restored by a run that is trusted with secrets or deploys.

| Run | Reads | Writes |
|---|---|---|
| A push, schedule, webhook or manual run on the default branch | `default` | `default` |
| Another branch, or a tag | its own scope, then `default` | its own scope |
| A pull request | `pull:<number>`, then `default` | `pull:<number>` |
| A draft run (unsaved pipeline text) | `default` | nothing |

So a pull request starts from what the default branch built, and saves only where the default
branch never looks. Nothing is shared between projects. An entry is never changed once saved: a key
that has an entry is not saved again. Launchway takes the default branch from the project's
repository; if it cannot tell which it is, no run writes `default` (a run of any branch then saves
to its own branch scope) rather than guess.

## What the step's log says

```text
Cache pip: restored key 3f9c… (exact, 118 MiB, 4 s)
Cache pip: no entry; the step runs without it
Cache pip: saved 120 MiB as key 3f9c…
Cache pip: not saved: an entry with this key was saved already, and entries are never changed
```

The same results are on the step in the run page, in `launchway runs show`, in the run's JSON
(`caches` on each step) and in the MCP tool `get_run`. A cache never changes a step's outcome: if it
cannot be restored (a damaged entry, a server that cannot be reached) or saved (over the limits,
caching off), the step runs on, and the log says why. A damaged entry is ended so that the next
step saves a good one.

## What is kept, and for how long

Only regular files and directories are kept: symbolic links are left out, and the log says which.
Entries are tar archives compressed with zstd. By default a project keeps 10 GiB of caches, one entry
may hold 2 GiB, and an entry nobody restored for 7 days is ended. An entry counts against the quota from the moment its
save begins, so uploads in progress take room too. When a save would pass the quota, the project's
least recently used entries are ended to make room, those of pull requests and branches before the
default branch's, or the save is refused if it cannot fit. An administrator changes these in the database, for the installation or for one project: the
**Caches** page of the project, `launchway caches policy`, `PUT /api/v1/cache-policy` and the MCP tool
`set_cache_policy` do the same thing, and every change is audited. With caching off for a project,
lookups miss and nothing is saved, and the pipeline still passes validation.

People with the operator role delete entries (one, or a whole cache by name) from the project's
**Caches** page, `launchway caches delete`, `DELETE /api/v1/projects/{name}/caches` and the MCP tool
`delete_cache`; viewers list them (`launchway caches list`, `list_caches`). The next run that wanted a
deleted entry starts without it and saves it again.

## Runners and older servers

Runners that know caches say so, and a step with `cache:` is only given to them. A pipeline with a
cache carries `requires: 25`, so servers older than that leave its runs alone; use the key only once
every server and runner is upgraded (see [database migrations](../development/migrations.md)).

See the [pipeline reference](../reference/pipeline.md#cache) for every key and limit.
