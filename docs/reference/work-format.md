# Work formats

Exact shapes for work tracking: the file that brings epics and stories in, the promotion of a repository's
read-only [archive](archive-format.md) into live items, and the settings that configure work at a level.
The guides are [tracking work](../guide/tracking-work.md), [boards and sprints](../guide/boards-and-sprints.md),
[work and code](../guide/work-and-code.md) and [working with agents](../guide/working-with-agents.md).

## Stories JSON

A file of epics and stories, as one JSON array, imported into one scope. Importing the same file again
changes nothing: items are matched by `key`.

```json
[
  { "key": "E01", "title": "Take payments", "labels": ["epic"] },
  {
    "key": "S01-01",
    "epic": "E01",
    "title": "Charge a card",
    "body": "**Goal**\n\nA customer can pay.\n\n**Acceptance criteria**\n\n- [ ] A card is charged once\n- [ ] A failed charge is shown\n\n**How to verify**\n\nRun the checkout test.",
    "labels": ["story", "area/payments"],
    "milestone": "Launch",
    "depends_on": []
  },
  { "key": "S01-02", "epic": "E01", "title": "Refund a payment", "depends_on": ["S01-01"], "milestone": "Launch" }
]
```

| Field | Type | Meaning |
|---|---|---|
| `key` | text, required | A stable name for the item in the file, unique in it. It is kept on the item (`source_key`) and is what makes a second import change nothing. Search it with `key:S01-01` |
| `title` | text, required | The title, cut at 300 characters |
| `epic` | text | The `key` of the epic this is under. A key that is in neither the project nor the file gives a warning and no parent |
| `body` | text | The description |
| `labels` | list of text | Lower-cased; those that are not valid labels are dropped; at most 50 |
| `milestone` | text | The title of a milestone, made if it does not exist |
| `depends_on` | list of keys | Items that must finish first. Each makes a "blocks" link from that item to this one |
| `rank`, `phase`, `doc` | | Accepted and ignored. Items are ordered as the file lists them, epics first |

**Type.** An item with an `epic` is a story. Without one, a key that is `E` followed by a digit is an
epic, `S` followed by a digit is a story, and anything else is an issue. Epics are made first so stories
find their parents.

**Acceptance criteria.** The lines `- [ ] text` (or `- [x]`) that follow a line holding only `**Acceptance
criteria**` (also `**Acceptance**` or `**Done when**`, in any case) become the item's criteria, up to
the next line that starts with `**`. At most 200.

**What an import returns.** Counts of what was created, what already existed, and links made, and
warnings (for example, a dependency on a key that is not there). A dependency that would make a cycle is
reported as a warning and skipped.

```text
POST /api/v1/work/import/stories
{"scope": "project:acme/shop", "items": [...], "preview": true, "update": false}
```

`preview: true` makes no change and reports what would happen. `update: true` also brings the title and description of items already
imported up to date from the file; nothing else of them is touched. A file holds 1 to 5,000 items. Only a person who maintains the scope
imports. MCP: `import_work_stories`. Each import is recorded in the Logbook as `work.import_stories`.

## Archive promotion

A repository's [archive](archive-format.md) holds another server's issues read-only. **Promoting** it turns
them into live items of the project:

| In the archive | Becomes |
|---|---|
| An issue | An item with **the same number**, keeping its title and description, state (open or closed), labels, milestone and original dates |
| An issue labeled `bug` | An item of type `bug`; everything else is an `issue` |
| The author | The reporter, as text; an issue with no author is reported by `unknown` |
| Assignees | Kept as the names given; they are not matched to accounts |
| Comments | Comments, with their authors and times as text |
| A pull request in the archive | Links on the issues it names (`#12`, `fixes #12`), with its state; a pull request is not an item |
| A full commit hash in the text | A link to the commit |

Promoting is repeatable: an issue already promoted is not made twice, and comments added to the archive
since are added. A number a live item already holds is reported as a conflict and left alone. With
`preview: true` it only counts.

```text
POST /api/v1/work/import/archive
{"repository": "acme/shop", "preview": true}
```

Only a maintainer of the project promotes; MCP: `promote_archive`; Logbook: `work.import_archive`.
Importers for other trackers than this are not built.

## Settings

Work is configured per level (Base, Wing, Squadron, Project). A field left out is inherited from the next
level up; a nearer level overrides a farther one.

```text
GET /api/v1/work/settings?scope=project:acme/shop     own, effective and autonomy
PUT /api/v1/work/settings   {"scope": "project:acme/shop", "settings": {...}}
```

`PUT` replaces the settings of the one level and is for a person who maintains it. The answer to `GET` has
`own` (what this level sets), `effective` (after inheritance, with the chain of levels and where the
autonomy policy comes from), `autonomy` (see below) and whether you may change it. Each change is recorded
as `work.settings`.

```json
{
  "prefix": "PAY",
  "estimate_unit": "points",
  "states": [
    { "key": "backlog", "name": "Backlog", "category": "todo" },
    { "key": "ready", "name": "Ready", "category": "todo" },
    { "key": "in_progress", "name": "In progress", "category": "doing", "wip": 5 },
    { "key": "in_review", "name": "In review", "category": "review" },
    { "key": "done", "name": "Done", "category": "done" },
    { "key": "wont_do", "name": "Won't do", "category": "cancelled" }
  ],
  "transitions": [
    { "to": "done", "who": "human" },
    { "from": ["in_review"], "to": "in_progress", "who": "anyone" }
  ],
  "types": [
    { "key": "bug", "name": "Bug", "template": "**Steps to reproduce**\n\n**What happened**\n\n**What you expected**" },
    { "key": "task", "name": "Task", "disabled": true }
  ],
  "fields": [
    { "key": "customer", "name": "Customer", "type": "text", "types": ["bug"] },
    { "key": "severity", "name": "Severity", "type": "select", "options": ["minor", "major"], "required": true, "types": ["bug"] }
  ],
  "close": { "on": "merge", "state": "done", "verbs": ["fixes", "closes", "resolves"] },
  "environments": ["dev", "prod"],
  "classes": { "small": ["small", "medium", "large"], "large": ["large"] },
  "difficulty_rules": [{ "label": "trivial", "difficulty": "small" }, { "min_estimate": 8, "difficulty": "large" }],
  "agents": { }
}
```

| Key | Meaning |
|---|---|
| `prefix` | Up to 10 capital letters or digits, no space, `-`, `#` or `/`. One level owns a prefix across the installation |
| `estimate_unit` | `points` or `hours` |
| `states` | 2 to 20 states: `key` (up to 40 characters, unique), `name`, `category` (`todo`, `doing`, `review`, `done`, `cancelled`) and an optional `wip` limit. At least one `todo` and one `done`. A nearer level's states replace the farther one's, with its transitions |
| `transitions` | Rules about moving **into** a state (`to`), optionally only `from` some states: `who` is `anyone`, `human` (a person, never an agent) or `maintainer` (a person who maintains the scope). The default makes `done` and `wont_do` `human` |
| `types` | Per type: `key` (`issue`, `bug`, `story`, `task`, `epic`), `name`, a `template` the description starts from, `disabled` for no new items of the type |
| `fields` | Custom fields: `key` (no space or dot), `name`, `type` (`text`, `number`, `date`, `select`, `boolean`), `options` for `select`, `required`, `types` it applies to (all if empty). Added to those of farther levels |
| `close` | What merging a pull request does: `on` is `merge` (default), `review` or `never`; `state` is the state a merge moves to (default `done`); `verbs` are the closing words (default `fixes`, `closes`, `resolves`) |
| `environments` | The Airfields (environments) the delivery status is meant to show, in order. Recorded; the delivery view does not use it to narrow the list yet |
| `classes` | Which agent classes (`small`, `medium`, `large`) may take items of each difficulty |
| `difficulty_rules` | Set the difficulty of a new item by a `label` it carries or by a `min_estimate` it reaches; the first rule that fits wins. Added to those of farther levels |
| `agents` | The [agent rules](#agent-rules) |
| `proposals` | How analyzers may file work: `max_items` (the most new items one source opens in one project in one Sortie (run); 0 means 10), `auto_accept` (a list of `{source, min_severity}`: sources whose findings of at least that severity become items without a review; severity is 0 to 4) and `reason` (required with `auto_accept`). See [proposals](#proposals) |

### Agent rules

```json
{
  "agents": {
    "enabled": true,
    "human_approval": true,
    "allow_merge": false,
    "reason": "",
    "autonomy": {
      "level": 2,
      "paths": ["docs/**"],
      "types": ["task", "issue"],
      "max_files": 10,
      "max_lines": 200,
      "pause_after": 3,
      "pause_days": 14,
      "reason": "documentation changes are low risk"
    },
    "review": {
      "mode": "independent_agent_allowed",
      "paths": ["docs/**"],
      "types": ["task"],
      "max_files": 10,
      "max_lines": 200,
      "require_different_model": true,
      "sample_percent": 10,
      "reason": "a second agent reads documentation changes before a person does"
    }
  }
}
```

| Key | Meaning |
|---|---|
| `enabled` | The Master switch (kill switch) of the level. `false` stops every agent there, and below. Absent means on |
| `human_approval` | Default `true`: an agent's work needs a person's approval. Setting it `false` needs a `reason` |
| `allow_merge` | Default `false`: an agent may not merge its own work. Setting it `true` needs a `reason` |
| `reason` | Why a safeguard was loosened. Recorded |
| `autonomy.level` | 0 to 4. 0 propose only; 1 a person approves and merges (default); 2 auto-merge for listed changes; **3 and 4 are accepted and stored, and the server enforces them as 2** |
| `autonomy.paths` | Patterns of the files an agent may change on its own; required at level 2 or above. `docs/**` means everything under `docs/` |
| `autonomy.types` | The item types it applies to; empty means any |
| `autonomy.max_files`, `max_lines` | Size limits; 10 and 200 when 0 |
| `autonomy.pause_after`, `pause_days` | Pause after this many reverted or failed changes within this many days; 3 and 14 when 0 |
| `autonomy.reason` | Required above level 1 |
| `review.mode` | `human_only` (default) or `independent_agent_allowed` |
| `review.paths`, `types`, `max_files`, `max_lines` | What an independent agent's approval may cover; `independent_agent_allowed` needs `paths` and a `reason` |
| `review.require_different_model` | The reviewer's declared model must differ from every author agent's |
| `review.sample_percent` | 0 to 100: the share of merges an agent approved that are flagged for a person to look at |

Pipeline files, everything under `.launchway/`, owners files, quarantine files, and the deletion or
skipping of tests always need a person, whatever these say.

`GET /api/v1/work/settings` reports the policy in force:

```json
{"level": 4, "enforced_level": 2, "available": false, "source": "installation", "paused": false, "failures": 0, "pause_after": 3, "agents_enabled": true}
```

`source` is the level it comes from (`default`, `installation`, `organization`, `unit:name`,
`project:owner/name`). `available` is `false` when `level` is above what the server acts on.

## Proposals

What an analyzer sends to `POST /api/v1/work/proposals` (the command `launchway work propose --from FILE`, the MCP tool `propose_work`). See [proposals from analysis](../guide/proposals-from-analysis.md).

```json
{
  "scope": "project:acme/shop",
  "source": "security-scan",
  "run_id": "2026-11-21.3",
  "report_url": "https://reports.example/acme/shop/3",
  "complete": true,
  "proposals": [
    { "dedupe_key": "dep:lodash:CVE-2021-23337", "group_key": "vulnerable-dependencies",
      "group_title": "Update {n} dependencies with known vulnerabilities",
      "title": "lodash 4.17.20 (CVE-2021-23337)", "severity": 3 }
  ]
}
```

| Key | Meaning |
|---|---|
| `source` | The analyzer's name; required. It is the namespace of the dedupe keys and the group keys |
| `run_id`, `report_url` | This Sortie, and where the full report is; kept on the item's `proposed` event |
| `complete` | The batch holds every finding of the source: findings left out are fixed. Without it, nothing is ticked off |
| `proposals[].dedupe_key` | The same finding has the same key in every Sortie |
| `proposals[].group_key` | Findings with the same key become one item with a checklist. Without one, a finding is its own group |
| `proposals[].group_title` | The item's title; `{n}` stands for the open occurrences |
| `proposals[].title`, `body` | This occurrence (a line of the checklist) and what is wrong |
| `proposals[].type`, `labels`, `priority` | Of the item; `type` is `issue`, `bug`, `story` or `task` |
| `proposals[].severity` | 0 none, 1 low, 2 medium, 3 high, 4 critical |
| `proposals[].evidence` | Links to what shows the finding |

The answer counts `received`, `new`, `updated`, `dismissed`, `fixed`, `waiting` (groups), `opened`, `folded`, `synced`, `closed` and `reopened`. At most 5000 findings in one batch. Items made from proposals carry the labels `machine-proposed` and `source:NAME`, have the reporter `source:NAME`, and start with a `proposed` event.
