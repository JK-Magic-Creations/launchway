# Artifacts

Files a step keeps: downloads for people, and inputs for later steps on any runner.

A step can keep files once it has run: people download them from the run, and later steps of
the run fetch them into their workspace, on whichever runner they run.

```yaml
steps:
  build:
    image: golang:1.27
    run: make dist
    artifacts:
      - name: binary
        paths: [bin/app, "dist/*.tar.gz"]   # globs as for reports; a directory keeps all below it
        retain: 30d                         # optional: 2w, 12h…; the server has a default and a maximum
        when: success                       # the default; or failure (screenshots, logs), or always

  deploy:
    image: alpine:3.22
    needs: [build]                          # the step must have run first
    fetch:
      - { from: build, name: binary, into: out }   # into is optional: default, where the files were
    run: ./out/bin/app --version
```

- **What is kept**: regular files only, copied out of the workspace by the runner and checked by
  SHA-256 end to end. Symbolic links are not followed. An artifact is kept whole or not at all: a
  file over the size limit, too many files, or paths that match nothing mean none is kept, and the
  step's log says why. Keeping files never changes the step's outcome.
- **Fetching** puts the files in the workspace before the step runs, on any executor, also when
  the step runs on another runner than the one that kept them, or in a re-run that reused the
  step. A fetch of an artifact that was not kept, has expired or was deleted fails the step and
  says so. A step can fetch only from steps it needs, directly or through others; in a template,
  from a step of the same template.
- **With the other features**: each member of a loop (`for_each`) keeps its own artifacts, so
  members may use the same name; fetching from a loop's members is not supported yet (fetch from
  a step that is not repeated). A step that runs after a failure (`when: { upstream: failed }` or
  `always`) may fetch from a step that failed what that step keeps with `when: failure` or
  `always`; one that runs only after a failure and fetches what is kept only on success is
  refused. Deploy and rollback steps take no `artifacts`, `fetch` or `reports`. Artifact names,
  paths and `into` are plain text: `{ input: … }` and `{ each: … }` are not allowed there (the
  script sees those values as `LAUNCHWAY_INPUT_<NAME>` and `LAUNCHWAY_EACH_<KEY>`).
- **Secrets are not masked in artifacts.** Secret values are hidden in a step's log, but files a
  step keeps are kept exactly as written. Do not put secrets in them.
- **The Artifacts tab** of a run lists each artifact with its files, sizes, SHA-256 and how long it
  is kept, with a download for each file and for all of them as a zip. Operators of the project may
  delete one; the audit trail records who did. Files are always served as downloads, never shown
  as pages.
- **Storage**: a directory (the default, under the data directory) for one server, or
  S3-compatible object storage, which several servers need. A file kept twice is stored once.
  Uploads go in 8 MiB chunks that any server can take, and carry on after a broken connection.
  Each server checks that it shares the store with the others (a small marker object), and keeps
  no artifacts, saying why, if it does not.
- **API**: `GET /api/v1/runs/{id}/artifacts`, `GET /api/v1/runs/{id}/artifacts/{artifact}/file?path=`
  (ranges supported), `GET /api/v1/runs/{id}/artifacts/{artifact}/archive` (zip),
  `DELETE /api/v1/runs/{id}/artifacts/{artifact}` (operators). Run summaries carry `artifacts`,
  the number kept.

| Setting | Default | Meaning |
|---|---|---|
| `artifacts.store` | filesystem | `filesystem` or `s3` |
| `artifacts.dir` | `<data>/artifacts` | the filesystem store's directory |
| `artifacts.s3.endpoint`, `bucket`, `region`, `prefix`, `path_style`, `ca_file` | – | the S3 store; credentials come from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or `*_FILE`), never from the file |
| `artifacts.max_file_mib` | 1024 | the largest file an artifact may hold |
| `artifacts.max_run_mib` | 4096 | how much one run may keep |
| `artifacts.max_project_gib` | 50 | how much a project may keep over all its runs |
| `artifacts.retain` | 30d | how long artifacts are kept when the pipeline does not say |
| `artifacts.max_retain` | 90d | the longest any is kept, whatever the pipeline says |

Expired artifacts are removed once their run has finished; contents nothing refers to any more are
removed an hour later. Every server does this; they never get in each other's way. A runner needs
disk space for a copy of what a step keeps or fetches. Runners older than this feature are not
given steps that keep or fetch artifacts. `make test-s3` checks the S3 store against a real
S3-compatible server.
