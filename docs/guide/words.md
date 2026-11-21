# The words Launchway uses

Launchway names its own levels and parts with a small set of words. This page lists each one, says what it means in plain terms, and shows how to change it.

## Why there are two sets

The documentation and the screens use the **default words**, which come from aviation: a change is a flight, so it has a plan, a checklist, a clearance and a Logbook. Some teams prefer plain business words, so a second set is built in, and any single word can be renamed. The choice is made when Launchway is first set up and can be changed at any time.

Two rules keep this safe:

- **Only labels change.** Web addresses, API paths, JSON fields, pipeline keys, CLI commands and flags, configuration kind names, audit actions and database columns keep their plain names in every set. A script, a pipeline file or a Terraform file never depends on a word you chose.
- **Words everyone knows are never themed.** Project, pipeline, step, run, environment, template, library, variable, branch, commit, pull request and secret mean the same in every set.

## The words

| Default word | Plain business word | What it means |
|---|---|---|
| **Base** | Installation | The whole Launchway installation: the top level |
| **Wing** | Organization | The group that owns the projects: the second level |
| **Squadron** | Team | An optional group of projects with shared settings: the third level |
| **Project** | Project | One repository and its pipeline: the fourth level |
| **Wingman** | Guide | The helper panel beside every page: where you are, and what you can do next |
| **Preflight** | Checks | The checks made before a deploy |
| **Checklist** | Checklist | The list of things that must be true before a change goes on |
| **Clearance** | Approval | A person decides whether a change may go on |
| **Go** / **No-go** | Approve / Reject | The two answers to a clearance. Go lets the run continue; No-go fails the step and skips what follows |
| **Hold** | Freeze | A time when changes may not go out |
| **Flight plan** | Starter pipeline | The ready-made pipeline for a kind of project |
| **Logbook** | Audit trail | The record of who did what, and when |
| **Payload** | Contents | What a change delivers |
| **In flight** | In progress | A change on its way to production |
| **Launch line** | Launch line | The line a change travels along, from commit to production (named after the product, so it is kept in both sets) |

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
