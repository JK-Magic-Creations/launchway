# The words Launchway uses

Launchway names its own levels and parts with a small set of words. This page lists each one, says what it means in plain terms, and shows how to change it.

## Why there are two sets

The documentation and the screens use the **default words**, which come from aviation: a change is a flight, so it has a plan, a checklist, a clearance and a Logbook. Some teams prefer plain business words, so a second set is built in, and any single word can be renamed. The choice is made when Launchway is first set up and can be changed at any time.

Three rules keep this safe:

- **Addresses are stable identifiers.** Page addresses, documentation file names and anchors never change with a word.
- **Only labels change.** Web addresses, API paths, JSON fields, pipeline keys, CLI commands and flags, configuration kind names, audit actions and database columns keep their plain names in every set. A script, a pipeline file or a Terraform file never depends on a word you chose.
- **Words everyone knows stay ordinary.** Pipeline, step, alert, branch, pull request, commit, secret, variable, template and library mean the same in every set. A themed word is used only where a newcomer can still understand it, and every one shows its plain meaning on hover and on this page.

## The words

<!-- vocabulary:guide -->
### Levels

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `installation` | **Base** | Installation | The whole installation, and every customer's own walled-off space on the hosted service: the top level. |
| `organization` | **Wing** | Organization | The group that owns the projects: the second level. |
| `unit` | **Squadron** | Team | An optional group of projects with shared settings: the third level. |
| `project` | **Project** | Project | One repository and its pipeline: the fourth level. |

### Work

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `runner` | **Station** | Runner | A machine or service that does the work of a pipeline's steps. |
| `runner_group` | **Flight line** | Runner group | A group of runners that share a purpose, such as every runner that can build images. |
| `run` | **Sortie** | Run | One execution of a pipeline, from the first step to the last. |
| `queue` | **Holding pattern** | Queue | Where runs wait until a runner or a slot is free, and the reason each one is waiting. |
| `run_log` | **Flight recorder** | Run log | The recorded output of every step of a run, kept so you can read what happened. |
| `debrief` | **Debrief** | Run summary | The report of a run's tests: what passed, what failed and why, which are flaky, and its exports. |
| `schedule` | **Patrol** | Scheduled run | A run that starts by itself at set times, such as every night. |

### Delivery

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `deploy` | **Launch / Launches** | Deploy | Put a version of your software into an environment, such as testing or production. |
| `environment` | **Airfield** | Environment | A place your software runs, such as development, testing or production. |
| `rollback` | **Return to base / Returns to base** | Rollback | Go back to the previous working version after a deploy. |
| `emergency_rollback` | **Abort** | Emergency rollback | Go back at once, skipping the checks and waits, when production is hurt. |
| `canary` | **Pathfinder** | Canary | A small first share of traffic or machines that gets the new version before everyone, to catch problems early. |
| `post_deploy_watch` | **Overwatch** | Post-deploy watch | The time after a deploy when Launchway keeps watching health signals and can go back by itself. |
| `toggle` | **Switch** | Feature toggle | A setting that turns a feature on or off without a new deploy. |
| `kill_switch` | **Master switch** | Kill switch | A switch that turns a feature off for everyone at once. |
| `release_unit` | **Formation** | Release group | Several things that are released together and move as one. |
| `preflight` | **Preflight** | Checks | The checks made before a run, a merge or a deploy. |
| `checklist` | **Checklist** | Checklist | The list of things that must be true before a change goes on. |
| `clearance` | **Clearance** | Approval | A person decides whether a change may go on, by saying Go or No-go. |
| `go` | **Go** | Approve | The recorded answer that lets the run continue. The button says Approve in both sets. |
| `no_go` | **No-go** | Reject | The recorded answer that fails the step. The button says Reject in both sets. |
| `hold` | **Hold** | Freeze | A time when changes may not go out. |
| `flight_plan` | **Flight plan** | Pipeline template | A complete, ready-made pipeline for a kind of project, with a few things to fill in. |
| `maneuver` | **Maneuver** | Step template | A reusable step, or a few steps, that a pipeline uses by name: build an image, deliver to an environment. |
| `hangar` | **Hangar** | Template library | The library of Flight plans and Maneuvers that ships with Launchway, and the page that shows it. |
| `payload` | **Payload** | Contents | What a change delivers. |
| `in_flight` | **In flight** | In progress | A change on its way to production. |
| `launch_line` | **Launch line** | Launch line | The path a change travels, from commit to production. Named after the product, so the same in both sets. |

### Platform

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `home` | **Tower** | Home | The first page: what needs you now, and a map of everything Launchway holds. |
| `monitoring` | **Radar** | Monitoring / Monitoring | Health, metrics and the map of how services talk to each other. |
| `notifications` | **Comms / Comms** | Notifications / Notifications | Messages Launchway sends when something needs attention, and where they go. |
| `registry` | **Depot** | Registry | Launchway's built-in store for container images and charts. |
| `artifact` | **Cargo / Cargo** | Artifact | A file or image a pipeline builds and keeps, such as a package or container image. |
| `standards` | **Standing orders / Standing orders** | Standards / Standards | The checks every repository gets without asking, and the gates that block a change that introduces a serious problem. |
| `scan` | **Inspection** | Security scan | The stage Standing orders add to every run: the security checks, then one verdict on what the change introduces. |
| `dependency_updates` | **Maintenance / Maintenance** | Dependency updates / Dependency updates | Proposed updates to the libraries a project uses, opened as pull requests. |
| `chaos_test` | **Drill** | Chaos test | A planned failure, such as stopping a service, to prove the system recovers. |
| `learn` | **Flight school** | Learn | Short lessons that teach Launchway by doing. |
| `first_run` | **In-processing** | Setup | The guided setup the first time Launchway starts. |
| `guide` | **Wingman** | Guide | The helper panel beside every page: where you are and what you can do next. |
| `logbook` | **Logbook** | Audit trail | The append-only record of who did what, and when. |

### Roles

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `owner` | **Commander** | Owner | Can do everything, including who may do what. The stored role is owner. |
| `approver` | **Controller** | Approver | Decides whether a change may go on. The stored role is approver. |
| `developer` | **Pilot** | Developer | Writes code and starts runs. The stored role is developer. |
| `viewer` | **Observer** | Viewer | Can read everything and change nothing. The stored role is viewer. |

### Statuses

| Key | Default word | Plain word | What it means |
|---|---|---|---|
| `queued` | **Holding** | Queued | Waiting for a runner or a free slot. |
| `running` | **Airborne** | Running | Working right now. |
| `succeeded` | **Mission complete** | Succeeded | Finished, and everything passed. |
| `failed` | **Failed** | Failed | Finished, and something did not pass. |
| `canceled` | **Scrubbed** | Canceled | Stopped on purpose before it finished. |
| `rolled_back` | **Returned to base** | Rolled back | Went back to the previous version. |
| `waiting` | **Waiting** | Waiting | Paused until a person decides or a condition is met. |
<!-- /vocabulary:guide -->

These pages and the screens agree: where this documentation says "clearance", the plain set says "approval". The pipeline key for a step that waits for a person is still `kind: approval` in every set.

## Choose or rename the words

In the first-run wizard the **Words** step offers the two sets. It previews the navigation, a trail through the levels, a decision card and the Wingman panel with your choice before anything is saved.

Later, an administrator opens **Settings → Words** to:

- switch between the aviation set and the plain business set;
- rename any single word, with a plural for the level words (for example "Company" for the second level, "Companies" in lists);
- reset one word, or every word, to the defaults.

A word is one line of at most 40 characters, with no `<`, `>` or `&`. The choice is saved in the `terms` entry of the [installation settings](configuration.md), so it is versioned and in the Logbook like any other change:

```yaml
terms:
  set: business            # aviation (the default) or business
  overrides:
    unit: Crew             # singular
    unit_plural: Crews     # optional; otherwise made from the singular
    guide: Co-pilot
```

The keys of `overrides` are `installation`, `organization`, `unit`, `project`, `guide`, `preflight`, `checklist`, `clearance`, `go`, `no_go`, `hold`, `flight_plan`, `maneuver`, `hangar`, `logbook`, `payload`, `in_flight` and `launch_line`; a key may end in `_plural`. An unknown key is refused. The same entry is read and written through the configuration API, the MCP tools and the CLI (`launchway config get installation`), like the rest of the installation settings.

Names you set under the older `levels` entry keep working: they apply under the chosen set and are replaced by a rename in `terms`. Saving the words from Settings folds them into `terms`.

## In reference pages

The reference pages (HTTP API, CLI, configuration objects, pipeline keys) use the fixed plain names, because those are what scripts depend on. Where they mention a level they use the configuration kind name, for example `organisation`; the American spelling `organization` is accepted as an alias for that kind in the CLI and the API.
