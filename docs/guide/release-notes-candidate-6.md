# Release notes: candidate 6

This candidate adds eight areas of work to what the previous build had: monitoring, custom steps, forks, a
trash for repositories, work with outside Git hosts, and the smaller features listed below. This page says what is
new, what behaves differently without anyone asking, how to upgrade and go back, and what to know about security.

## What is new

**Monitoring.** The server, each Station and the launch Station can serve their own metrics on a separate address
(never on the API port) in the text exposition format, with an optional bearer token. Trace export to a collector is
available for the server and for Stations. The chart has monitor values and autoscaling for the server. New
dashboards and alert rules ship in `deploy/dashboards`. Insights pages in the interface show the same numbers for an
installation, a team and a Flight. The mail outbox (pending and failed messages, and messages sent, retried and
given up) and the pulse of each Station loop (seconds since its last poll, actions running, actions given up after
their time) are metrics too. Fork operations, conflict resolutions and the outcomes of custom steps are counted, by
outcome only: no metric carries a name, an address or a person. The [metrics reference](../reference/metrics.md)
lists every one.

**Custom steps.** A step can be a directory with a manifest (`steps/<name>/step.yaml`) and code in any language,
with typed inputs and outputs, the permissions it asks for (network hosts, writable paths, secret inputs), a
sandbox, and a catalog page. A step of a library is used as `uses: <library>/<name>`; a step of the repository
itself as `uses: ./steps/<name>`. `launchway step` and `launchway steps` write, test and list them. See
[Write your own step](write-your-own-step.md) and the [manifest](../reference/step-manifest.md).

**Forks.** A hosted repository can be forked (when its owner allows forks) and a fork shares its parent's packs by
reference, so it costs a row per pack. Pull requests can come from a fork; a fork can be synced from its parent; a
repository can be a template that new repositories start from; conflicts of a pull request can be answered through
the API, the command line and the tools. See [Forks](forks.md) and [Templates](templates.md).

**Repository trash and addresses.** Deleting a repository puts it in a trash for a retention period (30 days by
default, a hosting setting) where an administrator can restore it or remove it for good; the removal is safe to
stop and repeat and never deletes a pack another repository (a fork or a parent) still names. A repository that is
renamed or moved keeps its old address working for Git (with a notice) and for the API and the pages (a redirect).

**Outside Git hosts.** A Flight can keep its code on a host Launchway does not own, through a connection: GitHub,
GitLab, Gitea or Forgejo, Bitbucket Cloud, Bitbucket Data Center, Azure DevOps, AWS CodeCommit, or any Git server
over HTTPS. Each host has a declared set of capabilities; the [capability matrix](outside-host-matrix.md) is
generated from them. "Cut a release" makes the tag and the release through the same group of calls for hosted and
outside repositories. Launch write-back works through a connection, including the host's own auto-merge where it has
one. The connection test tells apart a permission it verified, one that is lacking, and one it could not check.

**Repository pages and work.** Pull-request checks in place, stale branches, a branch made from a work item,
overview data (activity, languages, contributors), work items by repository, reactions, timers, an account summary
with sessions, a public profile, teams, an address on a deployment, and decision records.

## What changes by default

- A pull request from a fork always runs restricted, with the default branch's pipeline, whether the repository is
  hosted here or on an outside host, and whatever the setting for automation accounts says.
- The merge commit of a fork sync, the first commit of a repository made from a template, and the merge commit that
  resolving conflicts makes are signed by the installation's key when verified commits are on.
- A fork sync does not move a branch whose rule takes only verified commits; it says so and nothing changes.
- A deleted repository is no longer removed at once: it waits in the trash.
- A Station that does not speak the custom-step contract is not given a custom step.
- The Station's `/livez` fails when a loop that takes work has not polled for five minutes (introduced before this
  candidate; the metrics above show the same age).

## New for operators

- **Chart values:** `metrics` (enable, port, token), tracing endpoint values, server `autoscaling` (including
  `extraMetrics` and `behavior`), and monitor values. The Station chart opens a `metrics` port when metrics are on.
- **Configuration:** the `observability` section (metrics address and token, tracing endpoint, headers and sample
  ratio, `name_labels`) and a hosting setting for the trash retention.
- **Commands:** `launchway step`, `launchway steps`, `launchway account`, `launchway repos host ...`,
  `launchway repos fork-settings`, `pr checks`, `pr approve-run`. A Station takes `--metrics-listen` and
  `--allow-unsandboxed-steps`.
- **Paths without sign-in:** the metrics address (its own listener, optional bearer token), an outside host's
  webhook address for each connection (checked by the secret of the connection; a delivery without it is refused),
  and the capability matrix (declarations only; it needs sign-in but no connection).

## Upgrading and going back

Six migrations are applied, in this order, each only adding tables or columns:

| Schema | What it adds |
|---|---|
| 57 | outside-host connections |
| 63 | the record of custom step uses |
| 64 | forks and template repositories |
| 65 | pull requests from forks |
| 66 | the repository trash ledger |
| 67 | what the repository Overview keeps |
| 68 | reactions, time entries, decision records, deployment addresses |

(Schemas 58 to 62 came with the previous build.) Take a database backup first, as for any upgrade. The migrations
are additive: an older build of the server starts against the upgraded database and ignores what it does not know,
but it will not run a pipeline that uses a custom step, show forks or trash, or keep a deleted repository: those
need the new build. To go back, return the image and chart; nothing needs undoing in the database. Stations should
be upgraded with the server, because a Station of the previous build is not given custom steps.

A launch step's time limit now starts when a launch Station takes it, not when it was put in the Holding pattern, and a step nobody takes
fails after `queues.deploy_pickup_wait` (15 minutes by default) with a plain reason. All servers of an installation
should be upgraded together. A rolling update runs old and new servers side by side for a short time; while a server
of the previous release is still connected (PostgreSQL), the new server gives launch actions no short pickup
deadline, so the older server cannot expire a step that was taken and is running. The time limit is then enforced by
the Station until the update ends.

## What to watch after the upgrade

- The metrics listener starts only when enabled; if its address cannot be bound the server runs without it and logs
  a warning.
- `launchway_station_last_poll_age_seconds` growing past a minute on a Station that should be busy.
- The trash list after the first deletions, and the `repository.purge` lines of the Logbook once a retention ends.
- A connection to an outside host: run its test and read the three states (has it, lacks it, not checked).

## Security notes

- Metrics are served on their own address, never the API's; protect it with the bearer token or the network.
- Custom steps run with only the permissions their manifest names; a pipeline sees what a step may do before it is
  added; secret inputs are given by reference.
- A connection's credential is kept as the name of a secret; nothing logs it; every call to a host goes through the
  same egress guard as other outbound calls, and Git operations connect to the address that was checked.
- A webhook from an outside host is refused unless its secret or signature matches; a connection with no secret
  accepts nothing.
- A pull request from a fork never runs the pipeline it brings, runs restricted, and a
  first contribution waits for approval.
- A permanent removal of a repository never deletes data another repository still needs.

## For contributors

- Page texts that are long sentences are served as files under `web/src/prose/` and read with
  `await prose("pages.X")`; their keys are positional (`t0`, `t1`, ...), so a sentence added in the middle of a page is
  appended as a new key. A later pass should give the keys stable names.
- The Learn pages and this mechanism are two ways of serving text; they should be joined.
- Pages that came from several branches in this candidate still carry their texts in the source; opting them into the
  prose files is a size task for the next pass.
