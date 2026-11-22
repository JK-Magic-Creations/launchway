# Tracking work

Launchway keeps the work of a Flight (codebase) next to its code: issues, bugs, stories, tasks and epics, with a
small workflow, links between them, and links to the commits, pull requests, runs and deployments that
carry them out. Everything below works the same in the web interface, over the HTTP API, with the MCP
tools and with the command line. People and [agents](working-with-agents.md) use the same items and the
same rules.

Pages that go with this one: [boards and sprints](boards-and-sprints.md) for planning,
[work and code](work-and-code.md) for pull requests and delivery, and
[working with agents](working-with-agents.md) for work done by software. The exact query language is in
the [query reference](../reference/work-queries.md).

> [!NOTE]
> Email is not available. Launchway has no mail sender. What would be an email elsewhere is an entry in
> your [inbox](#the-inbox) and on Home, and, for other systems, the `work_item`
> [webhook event](../reference/events.md#work-item-events).

## From the command line

Everything on this page has a command: `launchway work items create "Fix the retry button" --type bug
--scope Flight:acme/shop`, `launchway work items list --query "assignee:me is:open"`, `launchway work items
comment PAY-12 "Seen on mobile"`, `launchway work inbox --unread`. The list is in the
[command-line reference](../reference/cli.md#launchway-work); `--json` gives the answer as the API
returns it.

## Where work lives

An item belongs to one **scope**: a Flight, or a level above it (a Squadron, a Wing, or the Base). Most
items live in a Flight. In the API a scope is written `project:owner/name`, `unit:name`, `organization`
or `installation`.

Each scope has a **prefix** of up to ten capital letters or digits, such as `PAY`, and counts its items
from 1. An item is named by the prefix and its number: `PAY-12`. A prefix belongs to one scope across the
whole installation, so `PAY-12` names exactly one item wherever it is written.

Who may do what follows the Flight's roles. Seeing work needs the viewer role, changing it the operator
role, and changing a scope's settings, registering agents and approving imports the admin role of the
Flight. See [Roles and permissions](../reference/roles.md#work-tracking).

## Types

| Type | Used for | May hold |
|---|---|---|
| `issue` | Something to look at: a request, a question, a problem not yet understood | tasks |
| `bug` | Something that does not work as it should | tasks |
| `story` | A change a user would notice | tasks |
| `task` | A piece of work | nothing |
| `epic` | A body of work made of stories, issues and bugs | stories, issues, bugs |

An epic has no parent. An item cannot be its own ancestor. A level may switch types off for new items;
existing items keep theirs.

## Workflow

Every state belongs to a **category**, and boards, charts and roll-ups read the category, not the name.
The default workflow is:

| State | Category | Meaning |
|---|---|---|
| Backlog | to do | Known, not yet ready to start |
| Ready | to do | May be started |
| In progress | doing | Someone is working on it |
| In review | review | Waits for a person to look |
| Done | done | Finished |
| Won't do | cancelled | Closed without doing it |

By default anyone who may change an item moves it anywhere, with one exception: **only a person moves
work to Done or Won't do**. An agent hands work over for review and a person finishes it. This is a rule
of the server, not of the screen; see [what the server enforces](working-with-agents.md#what-the-server-enforces).

A level can define its own workflow: 2 to 20 states, each with a key, a name and a category, at least one
to-do and one done state, an optional limit of cards per state (see
[boards](boards-and-sprints.md#work-in-progress-limits)), and **transitions**: for a state, who may move
work into it (`anyone`, `human` or `maintainer`), from which states. A nearer level replaces the workflow
of a farther one as a whole. The settings are described in the [work format reference](../reference/work-format.md#settings).

Finishing an item records when and by whom, and sets its resolution. The move is in the item's history
with the person's name.

## Fields of an item

| Field | Notes |
|---|---|
| Title | 1 to 300 characters, one line |
| Description | Text up to 256 KiB; new items of a type start from the type's template when none is given |
| Priority | `urgent`, `high`, `normal` (default), `low` |
| Estimate | A number from 0 to 100,000, in points or hours (a level's `estimate_unit`) |
| Start and due dates | A date or an RFC 3339 time |
| Labels | Up to 50, lower case, 1 to 60 characters, no comma or quote |
| Assignees | Up to 20 people or agents |
| Reporter | Who raised it; only a maintainer reports on someone's behalf |
| Watchers | People who are told when it moves |
| Milestone and sprint | See [boards and sprints](boards-and-sprints.md) |
| Difficulty | `small`, `medium` or `large`; decides which [class of agent](working-with-agents.md#class-routing) may take it |
| Custom fields | See [custom fields](#custom-fields) |

Saving an item may carry the version you read. If someone changed it meanwhile, the save is refused with
a conflict, and you read it again, so two people never overwrite each other's change unseen.

### Parent and children

Put a story, issue or bug under an epic, and a task under a story. The parent shows a **roll-up** of its
children: how many there are, how many are done, the points and the points done, how many are in
progress and how many are blocked.

### Dependencies and links

An item can **block** another, **relate** to it, or **duplicate** it. A blocking link has a direction; a
related link does not. Each link shows on both items.

A link that would make a cycle is refused, and the answer names the chain:
`that would make a cycle: PAY-3 blocks itself through PAY-3 blocks PAY-1 blocks PAY-2 blocks PAY-3`. An item cannot
be linked to itself, and the same link cannot be added twice. You may link items of different Flights
when you may see both.

An item blocked by an open item is marked **blocked** and shows by which. It is left out of what agents
are offered, and an agent cannot claim it. When the blocking item finishes, the people watching the
blocked one are told.

### Acceptance criteria

An item has a checklist of up to 200 criteria, each a line of text that is done or not. Criteria are how
a person or an agent says what finished means, and an [independent review](working-with-agents.md#independent-review-by-another-agent)
checks each one with evidence. When an item is imported from a story file, the checklist under the line
`**Acceptance criteria**` becomes its criteria ([format](../reference/work-format.md#stories-json)).

### Confidential items

A **confidential** item is seen only by people who maintain its scope, its reporter and its assignees.
Everyone else gets the answer they would get if it did not exist: it is missing from lists, boards,
charts, search, release notes and the links of other items. Only a maintainer changes whether an item is
confidential. An agent is never offered a confidential item and cannot claim one.

### Templates per type

Each type of a level can have a **template**: the description a new item of that type starts from, with
headings such as steps to reproduce for a bug. A template is only used when the person gives no description.

### Custom fields

A level can add fields to some or all types. A field has a key, a name and a type: `text`, `number`,
`date`, `select` (with its options) or `boolean`. A field can be required. An item holds at most 50 custom
values, and a value of the wrong type or outside a select's options is refused with a sentence that says
what the field takes. Fields of farther levels are added to the fields of nearer ones.

## Comments

Anyone who may see an item and change work in its scope may comment, and so may the reporter of an item
(even a confidential one). A comment is up to 64 KiB. Writing `@name` tells that person.

You may edit your own comment. **The earlier text is kept**: a comment that has been edited says so, and
its earlier versions are listed oldest first, with when each was written. Nobody edits another person's
comment. An agent comments only on the work it holds.

## Reactions

You may react to an item and to a comment with one of eight: `thumbs_up`, `thumbs_down`, `laugh`, `hooray`,
`confused`, `heart`, `rocket` and `eyes`. Anyone who can see the item may react, one of each kind per person
(reacting again changes nothing), and an agent does not. Every item and comment the API returns carries the
count of each reaction and whether it is yours (`launchway work items react REF heart`, the `react_to_work`
tool). Taking a reaction back removes only yours.

## Attachments

A file may be attached to an item. Each file is up to 10 MB and an item holds at most 50. Files are kept in
the database, with a SHA-256 of the contents, who added them and when. An attachment is a `file`, an
agent's `transcript`, or `evidence`.

Attachments are served as **downloads**, with sniffing off and scripts forbidden. Only PNG, JPEG, GIF and
WebP images and plain text may be shown in the page; every other type, including HTML and SVG, is
always a download. Add `?download=1` to the address to always get a download.

## History

Every change of an item is recorded with who made it (a person, an agent or the system), how (screen, API,
command line, MCP, Git or import) and what changed. The history is part of the item and cannot be
edited. Charts such as [cycle and lead time](boards-and-sprints.md#charts) are computed from it.

## Finding work

The search box, the list API and the `list_work_items` tool all take the same **query**: words in
titles and descriptions, and terms such as `state:in_progress type:bug -label:wontfix assignee:me
updated:<7d`. The language is in the [query reference](../reference/work-queries.md).

A query can be saved as a **view**. A view is private to you, or **shared** with everyone who reads the
scope (sharing needs the right to change work there). Views are listed on the work page, and are a name
and a query, nothing more: opening one runs the query again with what you may see now.

**Work of a repository.** `repo:acme/shop` in a query, `?repository=acme/shop` on the list route,
`--in-repo acme/shop` on the command line and `repository` on the tool find the repository's own work and the
work tied to its code (a branch, pull request, commit, run, or the repository itself). An item made with
`repository` and no scope lives in the repository's own scope (`launchway work items create --for-repo
acme/shop`); with another scope it is tied to the repository so that the repository's list finds it.

Lists are paged, sorted by backlog order unless you sort by update time, creation time, priority, due date
or number, and show only what you may see.

## The inbox

Your inbox lists what happened that concerns you, newest first:

| Entry | When |
|---|---|
| Assigned | Someone assigned an item to you |
| Mentioned | Someone wrote `@you` in a comment |
| Comment, state | An item you watch received a comment or moved |
| Unblocked | An item that blocked one you watch finished |
| Agent review | An agent handed an item over and a person is needed |
| Agent returned | An agent gave an item back, or its lease ran out |

You never get an entry for your own actions. Mark entries read one at a time or all at once. The count of
unread entries shows on Home. Agents have no inbox: they hear through [their list of work](working-with-agents.md#the-loop).

To tell something outside Launchway, subscribe a webhook to the `work_item` event of the repository
(see [webhooks](webhooks.md)).

## Releases and notes

`release_notes` (the function behind the HTTP route, the MCP tool and the command) lists the work finished
between two moments, grouped by type, with a Markdown text ready to paste: epics, stories, fixes (bugs),
issues, tasks. Items closed as Won't do and confidential items you may not see are left out. See
[work and code](work-and-code.md#release-notes).

## Importing

Work comes in from two places, both repeatable without making copies:

- a repository's read-only [archive](../reference/archive-format.md) of another server's issues becomes
  live items with the same numbers, comments and states;
- a **stories file** (JSON) of epics and stories becomes items with their parents, labels, milestones,
  acceptance criteria and dependencies.

Both can be previewed first. Only a maintainer imports. The shapes are in the
[work format reference](../reference/work-format.md). Importers for other trackers are not built.

## Proposals from analysis

A scan or a check does not file items one by one: it sends findings, which are grouped, capped and held for
a person to accept. See [proposals from analysis](proposals-from-analysis.md).

## What is not here

- No email, in or out.
- Attachments are kept in the database, not in object storage.
- Closing work from a pull request in another Flight is allowed only to people who hold a direct role
  there; see [work and code](work-and-code.md#closing-rules).
