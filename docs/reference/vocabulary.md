# Vocabulary

The glossary of record for the words Launchway uses for its own concepts. The words themselves, and how to change them, are explained for people on [The words Launchway uses](../guide/words.md); this page is the rulebook the documentation, the screens and the tools follow. The same list is data in [`docs/vocabulary.json`](https://github.com/JK-Magic-Creations/launchway/blob/main/docs/vocabulary.json), which `make vocabulary` reads.

These are ordinary words used descriptively. Launchway is not affiliated with or endorsed by any military service.

## The levels

The four levels have one permanent name in the API, flags and files, and a word people read. Prose uses the default word (Base, Wing, Squadron, Project); the plain word appears where the vocabulary is introduced, here, and next to a literal identifier in code font. "Installation" stays the ordinary word for a running Launchway.

<!-- vocabulary:levels -->
| Stable name (API, flags, files) | Default word | Plain word | What it is |
|---|---|---|---|
| `installation` | **Base** | Installation | the whole installation, the top level |
| `organization (kind name today: organisation)` | **Wing** | Organization | owns the projects; the second level |
| `unit` | **Squadron** | Team | an optional group of projects; the third level |
| `project` | **Project** | Project | one repository and its pipeline; the fourth level |
<!-- /vocabulary:levels -->

## The words

<!-- vocabulary:terms -->
| Default word | Plain word | Meaning | Renameable | Never call it |
|---|---|---|---|---|
| **Base** | Installation | The whole installation, and every customer's own walled-off space on the hosted service: the top level. | yes | tenant, workspace, account, instance, organization (for the top level) |
| **Wing** | Organization | The group that owns the projects: the second level. | yes | org, organisation, company (in product text), tenant |
| **Squadron** | Team | An optional group of projects with shared settings: the third level. | yes | group, namespace, workspace, team (when the default words are in effect) |
| **Project** | Project | One repository and its pipeline: the fourth level. | yes | repo (as the level), app (that is an application, a deployed thing) |
| **Wingman** | Guide | The helper panel beside every page: where you are and what you can do next. | yes | assistant, copilot, chatbot, bot |
| **Preflight** | Checks | The checks made before a run, a merge or a deploy. | yes | pre-checks, gate checks |
| **Checklist** | Checklist | The list of things that must be true before a change goes on. | yes | gate list |
| **Clearance** | Approval | A person decides whether a change may go on, by saying Go or No-go. | yes | sign-off, gate (for the thing a person gives) |
| **Go** | Approve | The recorded answer that lets the run continue. The button says Approve in both sets. | yes | none |
| **No-go** | Reject | The recorded answer that fails the step. The button says Reject in both sets. | yes | nogo, no go, NoGo |
| **Hold** | Freeze | A time when changes may not go out. | yes | blackout, code freeze (use freeze in the plain set) |
| **Flight plan** | Starter pipeline | A complete, ready-made pipeline for a kind of project, with a few things to fill in. | yes | blueprint, boilerplate, pipeline template (a step template is a different thing) |
| **Logbook** | Audit trail | The append-only record of who did what, and when. | yes | audit log |
| **Payload** | Contents | What a change delivers. | yes | delivery contents, manifest (that is a file) |
| **In flight** | In progress | A change on its way to production. | yes | none |
| **Launch line** | Launch line | The path a change travels, from commit to production. Named after the product, so the same in both sets. | yes | pipeline view (a pipeline is the definition; the launch line is the path of one change) |
| **Detachment** | Site agent (proposed) | The agent on the customer's side that does the work and holds the data, so the hosted service holds nothing sensitive. | yes | connector, delegate, relay |
<!-- /vocabulary:terms -->

Every word above is a default that an installation can rename. Words everyone knows are never themed: project, pipeline, step, run, branch, commit, pull request, secret, environment, deploy, rollback, variable, template, library, runner.

## The rules

<!-- vocabulary:rules -->
- **people-read.** What a person reads (screens, documentation, command help and output, the `error` and `next` sentences, MCP descriptions, notices, chart comments) uses the product words. Screens and messages the server builds ask the terms system ({key}, terms().label) so a rename takes effect; documentation names the default word and, once per page that introduces a level, the plain word beside it. Never a hard-coded plain level name where the level is meant.
- **identifiers.** Stable identifiers (API JSON fields and paths, database tables and columns, configuration keys and kind names, command-line flags, metric names, event kinds, Go identifiers) cannot follow a renameable word. Each level has one neutral, permanent name: installation, organization, unit, project. They are documented as 'the API name of a Base' and are never themed.
- **ordinary-words.** Where an industry word is the accepted term it stays ordinary: pipeline, step, run, branch, commit, pull request, secret, environment, deploy, rollback, variable, template, library, runner, artifact, registry.
- **buttons.** Open decision, not applied: the owner chose Clearance with Go and No-go, so the decision buttons say Go and No-go. A proposal to put the ordinary verbs Approve and Reject on the buttons, and keep Go and No-go as the recorded answer, is the owner's to take.
- **capitals.** Level words are the product's proper names for its levels: write Base, Wing, Squadron and Project capitalized when they mean the level (a lower-case 'base' or 'wing' reads as an ordinary word). Feature words are product nouns too: Wingman, Preflight, Clearance, Logbook, Flight plan, Detachment, Payload. 'launch line' stays lower case. (The screens' terms helper lower-cases a word in the middle of a sentence; that is an open decision.)
- **spelling.** American English: organization, color, behavior, license (noun and verb), initialize, customize.
- **levels-in-prose.** Where a level is meant, prose uses the default word (Base, Wing, Squadron, Project), never the plain word: 'organization' for the second level, 'unit' or 'team' for the third, 'installation' for the top level. The plain word appears once where the vocabulary is introduced, on the vocabulary page, and in code font where a literal identifier is shown (`organization`, `--organization`, JSON fields). 'Installation' stays the ordinary word for a running Launchway deployment. 'organization' stays where it means the real-world company ('your organization's identity provider'); those uses are on the allow-list.
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
