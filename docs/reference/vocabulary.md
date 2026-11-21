# Vocabulary

The glossary of record for the words Launchway uses for its own concepts. The words themselves, and how to change them, are explained for people on [The words Launchway uses](../guide/words.md); this page is the rulebook the documentation, the screens and the tools follow. The same list is data in [`docs/vocabulary.json`](https://github.com/JK-Magic-Creations/launchway/blob/main/docs/vocabulary.json), which `make vocabulary` reads.

These are ordinary words used descriptively. Launchway is not affiliated with or endorsed by any military service.

## The levels

The four levels have one permanent name in the API, flags and files, and a word people read. Prose uses the default word (Base, Wing, Squadron, Project); the plain word appears where the vocabulary is introduced, here, and next to a literal identifier in code font. "Installation" stays the ordinary word for a running Launchway.

<!-- vocabulary:levels -->
| Stable name (API, flags, files) | Default word | Plain word | What it is |
|---|---|---|---|
| `installation` | **Base** | Installation | The whole installation, and every customer's own walled-off space on the hosted service: the top level |
| `organization (kind name today: organisation)` | **Wing** | Organization | The group that owns the projects: the second level |
| `unit` | **Squadron** | Team | An optional group of projects with shared settings: the third level |
| `project` | **Project** | Project | One repository and its pipeline: the fourth level |
<!-- /vocabulary:levels -->

## The words

<!-- vocabulary:terms -->
### Levels

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `installation` | **Base** | Installation | The whole installation, and every customer's own walled-off space on the hosted service: the top level. | yes | tenant, workspace, account, instance, organization (for the top level) |
| `organization` | **Wing** | Organization | The group that owns the projects: the second level. | yes | org, organisation, company (in product text), tenant |
| `unit` | **Squadron** | Team | An optional group of projects with shared settings: the third level. | yes | group, namespace, workspace, team (when the default words are in effect) |
| `project` | **Project** | Project | One repository and its pipeline: the fourth level. | yes | repo (as the level), app (that is an application, a deployed thing) |

### Work

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `runner` | **Station** | Runner | A machine or service that does the work of a pipeline's steps. | yes | agent (for a Station), worker, node |
| `runner_group` | **Flight line** | Runner group | A group of runners that share a purpose, such as every runner that can build images. | yes | runner pool (in text a person reads), fleet |
| `run` | **Sortie** | Run | One execution of a pipeline, from the first step to the last. | yes | build (for any run), job (for a whole run), execution |
| `queue` | **Holding pattern** | Queue | Where runs wait until a runner or a slot is free, and the reason each one is waiting. | yes | backlog |
| `run_log` | **Flight recorder** | Run log | The recorded output of every step of a run, kept so you can read what happened. | yes | console output |
| `debrief` | **Debrief** | Run summary | The summary after a run: what passed, what failed and why, with the test report. | yes | post-mortem (for every run) |
| `schedule` | **Patrol** | Scheduled run | A run that starts by itself at set times, such as every night. | yes | cron job (in text a person reads) |

### Delivery

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `deploy` | **Launch / Launches** | Deploy | Put a version of your software into an environment, such as testing or production. | yes | release (for the act of putting live), push to production |
| `environment` | **Airfield** | Environment | A place your software runs, such as development, testing or production. | yes | stage (for a place), target |
| `rollback` | **Return to base / Returns to base** | Rollback | Go back to the previous working version after a deploy. | yes | revert (for a deployment), undo |
| `emergency_rollback` | **Abort** | Emergency rollback | Go back at once, skipping the checks and waits, when production is hurt. | yes | panic button, kill (for a deployment) |
| `canary` | **Pathfinder** | Canary | A small first share of traffic or machines that gets the new version before everyone, to catch problems early. | yes | test group |
| `post_deploy_watch` | **Overwatch** | Post-deploy watch | The time after a deploy when Launchway keeps watching health signals and can go back by itself. | yes | soak (for the watch) |
| `toggle` | **Switch** | Feature toggle | A setting that turns a feature on or off without a new deploy. | yes | feature flag (in text a person reads) |
| `kill_switch` | **Master switch** | Kill switch | A switch that turns a feature off for everyone at once. | yes | kill (for the act) |
| `release_unit` | **Formation** | Release group | Several things that are released together and move as one. | yes | bundle |
| `preflight` | **Preflight** | Checks | The checks made before a run, a merge or a deploy. | yes | pre-checks, gate checks |
| `checklist` | **Checklist** | Checklist | The list of things that must be true before a change goes on. | yes | gate list |
| `clearance` | **Clearance** | Approval | A person decides whether a change may go on, by saying Go or No-go. | yes | sign-off, gate (for the thing a person gives) |
| `go` | **Go** | Approve | The recorded answer that lets the run continue. The button says Approve in both sets. | yes | none |
| `no_go` | **No-go** | Reject | The recorded answer that fails the step. The button says Reject in both sets. | yes | nogo, no go, NoGo |
| `hold` | **Hold** | Freeze | A time when changes may not go out. | yes | blackout, code freeze (use freeze in the plain set) |
| `flight_plan` | **Flight plan** | Starter pipeline | A complete, ready-made pipeline for a kind of project, with a few things to fill in. | yes | blueprint, boilerplate, pipeline template (a step template is a different thing) |
| `maneuver` | **Maneuver** | Step template | A reusable step, or a few steps, that a pipeline uses by name: build an image, deliver to an environment. | yes | macro, action (that is another product's word), module |
| `hangar` | **Hangar** | Template library | The library of Flight plans and Maneuvers that ships with Launchway, and the page that shows it. | yes | marketplace, store |
| `payload` | **Payload** | Contents | What a change delivers. | yes | delivery contents, manifest (that is a file) |
| `in_flight` | **In flight** | In progress | A change on its way to production. | yes | none |
| `launch_line` | **Launch line** | Launch line | The path a change travels, from commit to production. Named after the product, so the same in both sets. | yes | pipeline view (a pipeline is the definition; the launch line is the path of one change) |

### Platform

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `home` | **Tower** | Home | The first page: what needs you now, and a map of everything Launchway holds. | yes | dashboard (for the first page) |
| `monitoring` | **Radar** | Monitoring / Monitoring | Health, metrics and the map of how services talk to each other. | yes | observability (in screens) |
| `notifications` | **Comms / Comms** | Notifications / Notifications | Messages Launchway sends when something needs attention, and where they go. | yes | none |
| `registry` | **Depot** | Registry | Launchway's built-in store for container images and charts. | yes | repository (for the store) |
| `artifact` | **Cargo / Cargo** | Artifact | A file or image a pipeline builds and keeps, such as a package or container image. | yes | build output |
| `standards` | **Standing orders / Standing orders** | Standards / Standards | The rules every project must meet: policies, required checks and approvals. | yes | compliance rules |
| `scan` | **Inspection** | Security scan | A check of your code and images for known security problems. | yes | none |
| `dependency_updates` | **Maintenance / Maintenance** | Dependency updates / Dependency updates | Proposed updates to the libraries a project uses, opened as pull requests. | yes | none |
| `chaos_test` | **Drill** | Chaos test | A planned failure, such as stopping a service, to prove the system recovers. | yes | none |
| `learn` | **Flight school** | Learn | Short lessons that teach Launchway by doing. | yes | none |
| `first_run` | **In-processing** | Setup | The guided setup the first time Launchway starts. | yes | none |
| `guide` | **Wingman** | Guide | The helper panel beside every page: where you are and what you can do next. | yes | assistant, copilot, chatbot, bot |
| `logbook` | **Logbook** | Audit trail | The append-only record of who did what, and when. | yes | audit log |

### Roles

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `owner` | **Commander** | Owner | Can do everything, including who may do what. The stored role is owner. | yes | none |
| `approver` | **Controller** | Approver | Decides whether a change may go on. The stored role is approver. | yes | none |
| `developer` | **Pilot** | Developer | Writes code and starts runs. The stored role is developer. | yes | none |
| `viewer` | **Observer** | Viewer | Can read everything and change nothing. The stored role is viewer. | yes | none |

### Statuses

| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|---|
| `queued` | **Holding** | Queued | Waiting for a runner or a free slot. | no | none |
| `running` | **Airborne** | Running | Working right now. | no | none |
| `succeeded` | **Mission complete** | Succeeded | Finished, and everything passed. | no | none |
| `failed` | **Failed** | Failed | Finished, and something did not pass. | no | none |
| `canceled` | **Scrubbed** | Canceled | Stopped on purpose before it finished. | no | none |
| `rolled_back` | **Returned to base** | Rolled back | Went back to the previous version. | no | none |
| `waiting` | **Waiting** | Waiting | Paused until a person decides or a condition is met. | no | none |
<!-- /vocabulary:terms -->

Every word above is a default that an installation can rename. Words everyone knows are never themed: project, pipeline, step, run, branch, commit, pull request, secret, environment, deploy, rollback, variable, template, library, runner.

## The rules

<!-- vocabulary:rules -->
- **people-read.** What a person reads (screens, documentation, command help and output, the `error` and `next` sentences, MCP descriptions, notices, chart comments) uses the product words. Screens and messages the server builds ask the terms system ({key}, terms().label) so a rename takes effect; documentation names the default word and, once per page that introduces a level, the plain word beside it. Never a hard-coded plain level name where the level is meant.
- **identifiers.** Stable identifiers (API JSON fields and paths, database tables and columns, configuration keys and kind names, command-line flags, metric names, event kinds, Go identifiers) cannot follow a renameable word. Each level has one neutral, permanent name: installation, organization, unit, project. They are documented as 'the API name of a Base' and are never themed.
- **ordinary-words.** Where an industry word is the accepted term it stays ordinary: pipeline, step, alert, branch, pull request, commit, secret, variable, template, library. Everything else Launchway names itself (runner, run, queue, deploy, environment, rollback, canary, registry, artifact and the rest of the table) uses the product word, with the plain word beside it where the word is introduced.
- **buttons.** A button for a delivery action uses the product verb: Launch (deploy), Return to base (rollback), Abort (emergency rollback). The plain word is in the button's accessible description and in the confirmation text. Elsewhere an ordinary verb on a button stays acceptable where a newcomer must understand it (Save, Cancel, Approve, Reject).
- **capitals.** Product words are capitalized as names, in pages as well as documents: Sortie, Station, Airfield, Launch, Tower, Radar. Level words (Base, Wing, Squadron, Project) and feature words (Wingman, Preflight, Clearance, Logbook, Flight plan, Detachment, Payload) too. The business set reads as ordinary English: its words are lower case in the middle of a sentence, except the level words. 'launch line' stays lower case.
- **spelling.** American English: organization, color, behavior, license (noun and verb), initialize, customize.
- **levels-in-prose.** Where a level is meant, prose uses the default word (Base, Wing, Squadron, Project), never the plain word: 'organization' for the second level, 'unit' or 'team' for the third, 'installation' for the top level. The plain word appears once where the vocabulary is introduced, on the vocabulary page, and in code font where a literal identifier is shown (`organization`, `--organization`, JSON fields). 'Installation' stays the ordinary word for a running Launchway deployment. 'organization' stays where it means the real-world company ('your organization's identity provider'); those uses are on the allow-list.
- **statuses.** A status is always shown with an icon and its plain word beside the themed one ('Airborne · running'); in the business set only the plain word. Never color alone.
- **no-combat.** Flying and ground operations only. No strike, target, kill, weapon or attack words and no real program, unit or rank names are product words.
- **stable-addresses.** Page addresses (URLs, routes), documentation file names, anchors, flags, API fields and enum values, JSON output, and stored role names never change with a word.
<!-- /vocabulary:rules -->

## Words of release management

Release management uses a few more product words. They are not renameable yet, and they are written as shown wherever a person reads them; identifiers (API paths and fields, flags, files) stay plain.

| Word | Means | In identifiers |
|---|---|---|
| **Airfield** | a place a release runs: what the configuration calls an environment | `environment`, `airfield` in release routes and inputs |
| **Launch** | a delivery reaching an Airfield: what is ordinarily called a deploy | `deploy`, `deployment` |
| **Return to base** | putting an Airfield back on the release before: a rollback | `rollback`, `release-returns` |
| **Cargo** | the files a step keeps, and what a release carries: artifacts | `artifacts`, `assets` |
| **Formation** | releases of several projects promoted together in an order | `formation` |
| **Sortie** | one run, as a release names it | `run`, `run_id` |

"Release", "tag", "version" and "changelog" stay ordinary words. Clearance, the checklist and the Hold (the plain word is freeze) come from the terms system like the rest.

## The two layers

**What a person reads** uses the words above, through the terms system, so a rename takes effect. **Stable identifiers** do not change with a rename: API paths and JSON fields, database tables and columns, configuration keys and kind names, command-line flags, metric names, event kinds and Go names. Each level has one permanent name there: `installation`, `organization`, `unit` and `project`. A script never depends on a label.

## The check

`make vocabulary` (part of `make check`) scans what people read in the documentation, the web interface, the command-line help, the API's sentences and MCP descriptions, the chart comments and the roadmap data. It fails when a forbidden word comes back. Existing uses are counted in `quality/vocabulary-baseline.json`, which may only shrink; justified uses (the terms system itself, another product's own word) are in `quality/vocabulary-allow.json`. `scripts/vocabulary-fix` makes the mechanical replacements and lists what needs a person; run it again after merging other work. `scripts/vocabulary-fix --docs` rewrites the tables on this page from the data.

<!-- vocabulary:checks -->
| Rule | Kind | Use instead |
|---|---|---|
| `british-spelling` | fails the check | the American spelling (organization, color, behavior, license, initialize, customize ...) |
| `org-shorthand` | fails the check | the level word through the terms system ({organization}), or write the plain word |
| `tenant` | fails the check | the top level word ({installation}: Base) or 'customer' |
| `audit-log` | fails the check | the Logbook ({logbook}) or, in plain description, the audit trail |
| `detachment-synonym` | fails the check | Detachment |
| `wingman-synonym` | fails the check | the guide ({guide}: Wingman) |
| `assistant-in-screens` | fails the check | the guide ({guide}: Wingman); 'assistant' stays only for a third-party AI assistant that connects through MCP |
| `hardcoded-product-word` | fails the check | the terms system: {guide}, {unit}, {preflight}, {clearance}, {logbook}, {flight_plan}, {payload}, {no_go} |
| `hardcoded-level-word` | review list | the terms system when the level is meant ({organization}, {installation}); ordinary use (an organization's identity provider; installing software) may stay and goes on the allow list |
| `approval-for-clearance` | review list | clearance for the thing a person gives; approval stays for pull-request reviews and the step key |
| `template-ambiguity` | review list | flight plan (a whole ready-made pipeline) or step template (a reusable part) |
| `level-organization` | fails the check | Wing (the second level) or, for the real-world company, add the use to the allow-list |
| `level-unit` | fails the check | Squadron (the third level) |
| `level-installation` | fails the check | Base (the top level); 'installation' stays for a running deployment |
| `level-lowercase` | fails the check | Base, Wing and Squadron are proper names: capitalize them |
| `feature-lowercase` | fails the check | capitalize the product noun (Wingman, Logbook, Detachment, Flight plan) |
| `launch-line-capital` | fails the check | launch line stays lower case |
| `the-audit-trail` | fails the check | the Logbook |
| `team-as-level` | review list | Squadron when the third level is meant |
| `installation-as-level` | review list | Base when the top level is meant |
| `clearance-preflight-lowercase` | review list | capitalize when the product concept is meant (webhook payloads and CORS preflight are ordinary) |
<!-- /vocabulary:checks -->
