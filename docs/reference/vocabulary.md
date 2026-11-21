# Vocabulary

The glossary of record for the words Launchway uses for its own concepts. The words themselves, and how to change them, are explained for people on [The words Launchway uses](../guide/words.md); this page is the rulebook the documentation, the screens and the tools follow. The same list is data in [`docs/vocabulary.json`](https://github.com/JK-Magic-Creations/launchway/blob/main/docs/vocabulary.json), which `make vocabulary` reads.

These are ordinary words used descriptively. Launchway is not affiliated with or endorsed by any military service.

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
- **buttons.** Ordinary verb on the button, product noun for the thing: the button says Approve or Reject; the thing a person gives is a clearance; the recorded answer is Go or No-go. Pull-request reviews keep the ordinary word approval; the pipeline key stays kind: approval.
- **capitals.** Level and feature words are common nouns: lower case in the middle of a sentence (the base, a wing, a flight plan), capitalized at the start of a sentence, in a heading, a label or a button. Wingman and Launchway are names: always capitalized.
- **spelling.** American English: organization, color, behavior, license (noun and verb), initialize, customize.
<!-- /vocabulary:rules -->

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
| `level-capitalized-mid-sentence` | review list | lower case in the middle of a sentence |
| `approval-for-clearance` | review list | clearance for the thing a person gives; approval stays for pull-request reviews and the step key |
| `template-ambiguity` | review list | flight plan (a whole ready-made pipeline) or step template (a reusable part) |
<!-- /vocabulary:checks -->
