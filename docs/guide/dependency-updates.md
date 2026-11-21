# Keeping dependencies up to date

Launchway finds the dependencies a repository uses, finds the ones that are out of date or have a known
vulnerability, and proposes each update as an ordinary **pull request**. Nothing is changed behind your back: a
person reviews the pull request, the pipeline checks it like any other, and a person merges it. You need no
outside update robot.

This page is how it works and how to use it. The settings and the file are in
[Dependency updates: settings and file](../reference/dependency-updates.md).

## What it covers

| Available | Read from | How the update is made |
|---|---|---|
| Go modules | `go.mod`, `go.sum` | `go get` and `go mod tidy`, so `go.mod` and `go.sum` are written by the Go tool |
| npm | `package.json`, `package-lock.json` | the range in `package.json` is rewritten, then `npm install --package-lock-only --ignore-scripts` writes the lock file |
| Python | `requirements*.txt`, `pyproject.toml`, `uv.lock`, `poetry.lock` | a pinned requirement is rewritten; `uv.lock` is written by `uv lock`; for hashes and Poetry the pull request says the lock must be regenerated |
| Container images | `FROM` lines of container build files (multi-stage builds, `ARG` defaults), `image:` in Launchway pipelines, and image variables in files you name | the tag, and the digest if the line pins one, is replaced |
| Workflow files | `uses:` lines in `.github/workflows` | the tag, or the commit with its version comment, is replaced |

Coming, not available yet: Maven and Gradle, Cargo, .NET, Helm chart dependencies and Terraform providers.
pnpm and Yarn lock files are detected and listed as "found, not updated", so nobody thinks they are covered.

A dependency that only a lock file lists (needed by another package) is shown, and its vulnerabilities are
reported, but it is never edited on its own: update the package that needs it.

## What it proposes

- **Security first.** A dependency with a known vulnerability and a published fix is proposed at once, outside
  the schedule, on its own pull request, with the advisory, its severity and the fixed version in the
  description. It moves to the lowest version that fixes it.
- **A wait for new releases.** Other updates are proposed only after the version has been public for
  three days (a setting). A version published a minute ago might be withdrawn or tampered with; a security fix is
  not held back, because waiting is the greater risk.
- **Grouped.** Minor and patch updates of one ecosystem and directory share one pull request; a major update is
  always its own. You can name groups by pattern.
- **Limited.** At most three update pull requests are open at a time, and a run opens at most three. A burst
  of updates waits and says why. Security fixes are not held to the number open, only to what one run opens.
- **One pull request per dependency or group, kept up to date.** When a newer version appears, or the default
  branch moves, the same pull request is rebuilt in place. When the update is no longer needed (merged another way,
  or the dependency was removed) it is closed with a note.
- **Never again what you turned down.** Close a pull request with a comment: `ignore this version` (also covers
  older versions), `ignore this major`, or `ignore this dependency`. A plain close means "this version".

Go's rule that version 2 and later are different module paths is respected: the new major version of a Go
module changes the import paths in your code, so it is shown in the dependency view as "a new major version
exists" and left to a person.

## Where the lock files come from

The server never runs a package manager. The updater edits the manifest as text and then runs the ecosystem's
own tool on a copy of the files, inside a step container on a runner, with no secrets and only the registries your
installation allows. It takes back only the files that were meant to change (a manifest and its lock file) and
refuses anything else. The pull request names the exact command and the image it ran in. Pin the toolchain
images by digest and mirror them in the settings key `toolchains`.

## Seeing it

Open a hosted repository and choose **Dependencies**. The table shows each dependency's version in use, the newest
version, the one the settings would move to, how old that version is, its known vulnerabilities with severity, the
open pull request, and why nothing is proposed when that is so. **Updates and settings** shows the settings that
apply (and which level locked what) and, on request, a preview of what the updater would open now. The same
information is in the API (`GET /api/v1/repositories/{owner}/{name}/dependencies` and `/updates/preview`), the MCP tools
`list_dependencies` and `preview_updates`, and the command line:

```sh
launchway deps list -R acme/shop                 # what it depends on
launchway updates preview -R acme/shop           # what would be opened
launchway deps check --dir . --sarif deps.sarif  # known vulnerabilities, for the Security tab
launchway deps ignore tinyfmt --reason "pinned until the vendor fixes 2.x"
```

## Running it

The updater runs as a step of a pipeline on a runner with a token that may push branches and open pull requests,
so the pull requests it opens are ordinary and the runner, not the server, runs the package tools:

```yaml
pipeline: dependency-updates
on:
  schedule: "0 6 * * 1"
steps:
  updates:
    image: ghcr.io/example/launchway:1   # an image with the launchway command and podman access
    run: launchway updates run -R acme/shop --executor podman
```

Use the schedule you chose in the settings. A new repository gets security updates on and routine updates on with
grouping. When you turn updates on for a repository that already exists, look at `launchway updates preview`
first: it lists exactly what would be opened, and nothing is opened by looking.

`launchway updates run --dry-run` does everything but change something.

## Registries, mirrors and offline use

The server and the runner ask the Go module proxy, the npm registry, PyPI and container registries for versions,
and an OSV service for vulnerabilities. Every request goes through the installation's egress rules, and answers
are cached for an hour. To use your own mirrors, or none at all, set `LAUNCHWAY_UPDATES_GOPROXY`,
`LAUNCHWAY_UPDATES_NPM_REGISTRY`, `LAUNCHWAY_UPDATES_PYPI`, `LAUNCHWAY_UPDATES_OCI_MIRRORS`,
`LAUNCHWAY_UPDATES_OSV_URL` or `LAUNCHWAY_UPDATES_OSV_DIR` (a directory of OSV records, one JSON file each) and
`LAUNCHWAY_UPDATES_OFFLINE=1` so that only those are used. The built-in package registry, when it exists, becomes one
more source with its address in the same place.

## What it does not do yet

Settings are the repository's file and the levels' settings; there is no form to edit them in the web interface
yet, so the page shows them and never offers a button that would not work. The pull request links to release
notes where the registry gives a link, but does not quote a changelog. Automatic merging is off and not built; it
will be available only where a level allows it, once the required checks pass, for the kinds you name.
