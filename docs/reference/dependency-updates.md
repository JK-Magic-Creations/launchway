# Maintenance (dependency updates): settings and file {#dependency-updates-settings-and-file}

How [Maintenance](../guide/dependency-updates.md) are configured. Settings come from layers, widest
first: the built-in defaults, the Base, the Wing, the Flight (codebase), the repository's settings, and the
repository's own file `.launchway/updates.yaml`, read from the **default branch only** (a branch cannot loosen
the settings). A level can **lock** a key: no narrower level and not the file may change it, and the file's
attempt is listed as "not applied". The file cannot lock anything; if it tries, it is refused.

## The file

```yaml
version: 1                 # required
schedule: {interval: weekly, day: monday, hour: 6, timezone: America/Chicago}
cooldown_days: 3
limits: {max_open: 6, max_per_run: 3, max_open_per_ecosystem: 2}
ecosystems:
  go: {enabled: true}
  python: {enabled: false}
groups:
  - {name: minor-and-patch, ecosystem: go}
  - {name: minor-and-patch, ecosystem: npm, directory: web}
ignore:
  - {name: left-pad, reason: pinned on purpose}
  - {name: "golang.org/x/*", kinds: [major]}
constraints:
  - {name: vite, major: 5}
exclude: ["examples/*"]
image_files: [scripts/security/*.sh]
labels: [dependencies]
```

An unknown key is an error that names it. Every problem found is reported together.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Updates for this repository at all |
| `security` | `true` | Security fixes, opened at once outside the schedule |
| `routine` | `true` | Other updates, grouped, within the limits |
| `ecosystems` | all five on | Per ecosystem (`go`, `npm`, `python`, `container`, `workflow`): `enabled`, and `directories` to limit where to look |
| `schedule` | weekly, Monday, 06:00 UTC | `interval` (`daily`, `weekly`, `monthly`), `day`, `hour`, `timezone`. The schedule of the pipeline that runs `launchway updates run` |
| `cooldown_days` | `3` | How long a version must have been public before a routine update proposes it (0 to 90). Not applied to security fixes |
| `groups` | one `minor-and-patch` group | Updates collected into one pull request: `name`, `ecosystem`, `directory`, `patterns` (name globs), `kinds` (default patch and minor; list `major` to group majors). A major update not in such a group is its own pull request |
| `limits` | 3 open, 3 per Sortie (run) | `max_open` (open update pull requests), `max_per_run`, `max_open_per_ecosystem` (0: none). `0` for the first two opens nothing |
| `allow` | everything | Only dependencies matching these name globs |
| `ignore` | none | Rules `name` (glob), `versions` (range of target versions), `kinds`, `reason` |
| `constraints` | none | Keep a dependency on a major (`major`) or inside a `range` |
| `auto_merge` | off | Reserved: automatic merging is not available yet |
| `commit` | `Update`, `launchway/update` | `message_prefix` and `branch_prefix`. Branches outside the prefix are never pushed to |
| `labels`, `reviewers`, `assignees` | `dependencies` | Applied to every pull request |
| `prereleases` | `false` | Propose pre-releases (a dependency already on one may follow its line) |
| `image_files` | none | Files (globs) whose `NAME_IMAGE='ref'` lines are image references to keep current |
| `exclude` | none | Paths (globs, `*` matches any run of characters) that are not looked at |
| `toolchains` | Go 1.27, Node 24, uv | Images the lock files are written in: keys `go`, `node`, `uv`. Pin by digest and mirror them |

Name globs are case-insensitive and `*` matches any run of characters, including `/`.

## Rules of the proposals

- Branch `launchway/update/<key>`, where the key is stable, so one pull request exists per dependency or
  group: `go-minor-and-patch`, `npm-minor-and-patch-web`, `go-security-<module>`, `npm-<name>-major-web`.
- A pull request carries a line at the end of its description that Launchway reads back; do not remove it.
- Close with a comment `ignore this version` (this and every older version), `ignore this major` or
  `ignore this dependency`.
- The updater never pushes to the default branch or to a protected branch, and replaces only its own branches.

## Airfield (environment) {#environment}

| Variable | Meaning |
|---|---|
| `LAUNCHWAY_UPDATES_GOPROXY` | Go module proxy (default the public one) |
| `LAUNCHWAY_UPDATES_NPM_REGISTRY` | npm registry |
| `LAUNCHWAY_UPDATES_PYPI` | A PyPI JSON service |
| `LAUNCHWAY_UPDATES_HOST_API` | The code host's REST address for workflow action tags |
| `LAUNCHWAY_UPDATES_OCI_MIRRORS` | `registry=https://mirror,registry2=https://mirror2` |
| `LAUNCHWAY_UPDATES_OSV_URL`, `LAUNCHWAY_UPDATES_OSV_DIR` | OSV service, or a directory of OSV records (wins) |
| `LAUNCHWAY_UPDATES_OFFLINE` | `1`: use only the addresses above, never a public default |
| `LAUNCHWAY_UPDATES_CACHE_TTL` | How long Depot (registry) answers are kept (default `1h`; `0s` keeps none) |
| `LAUNCHWAY_UPDATES_ENV_<NAME>` | On a Station (runner): passed to the lock step as `<NAME>` (for example `GOSUMDB`) |

## Interfaces

| What | HTTP | MCP | Command line |
|---|---|---|---|
| The dependencies | `GET /api/v1/repositories/{owner}/{name}/dependencies` | `list_dependencies` | `launchway deps list` |
| What would be opened | `GET /api/v1/repositories/{owner}/{name}/updates/preview` | `preview_updates` | `launchway updates preview` |
| Run the updater | pull request routes | — | `launchway updates run`, `launchway deps update` |
| Vulnerabilities for the Security tab | — | — | `launchway deps check --sarif` |
| Settings that apply | in both answers above (`policy`) | same | `launchway updates settings` |
| Ignore a dependency | the file | — | `launchway deps ignore` |

Reading needs the viewer role on the repository; the updater's token needs the operator role. Opening, updating and
closing pull requests are recorded like any pull request (`pull.create`, `pull.update`, `pull.close`).
