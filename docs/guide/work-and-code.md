# Work and code

An item of work is tied to the code that carries it out and to where that code is running. Nobody has to
copy a link by hand: writing the item's name in a commit message, a branch name or a pull request does
it, and the item then shows its branches, pull requests, commits, runs and **where the fix is running**.

This page covers hosted repositories (`forge.hosting` on); work tracking itself does not need them. For
the items see [tracking work](tracking-work.md); for what agents may and may not do with pull requests see
[working with agents](working-with-agents.md).

## Naming work in code

Launchway reads the title and description of a pull request, its branch name, and the messages of its
commits, and pushes to a hosted repository. It finds item names in three forms:

| Form | Names |
|---|---|
| `#12` | Item 12 of the project the pull request or push is in |
| `PAY-12` | The item with that prefix and number, in whichever project owns the prefix |
| `acme/shop#12` | Item 12 of that project |

A bare number glued to a word (`C#12`) is not a reference. Names that match no item are reported as
unknown and ignored.

The word before a name decides what happens:

| Words | Effect |
|---|---|
| `fix`, `fixes`, `fixed`, `close`, `closes`, `closed`, `resolve`, `resolves`, `resolved` | **Links, and closes** by the rule below |
| `refs`, `ref`, `references`, `see`, `part of`, `related to`, `relates to`, or no word | **Links only**; nothing moves |

A word applies to a list that follows it: `fixes #1, #2` closes both. Which words close is itself a
setting of the level (`close.verbs`), and a level may use only `fixes`, for instance. A branch named after
an item, such as `PAY-12-retry-button`, is read too: the pull request or the commits pushed on it are linked to the item.

## What an item shows

An item lists the code tied to it, each with its state:

| Kind | Shown as | States | Linked |
|---|---|---|---|
| Pull request | `acme/shop#34` with its title | open, merged, closed | by the names above, when it opens and each time it changes |
| Commit | A short hash with its subject | the branch it reached, or merged | by the names above, on a push; the merge commit when a linked pull request merges |
| Branch | The branch name | | by hand or by an agent |
| Run | The pipeline run for the change | success, failed | by hand or by an agent |

Add a link yourself with `link_work_code` or `POST /api/v1/work/items/{ref}/code` (kind `commit`, `branch`, `pull` or `run`, and
its reference). Linking again updates the title and state instead of adding a second link.

and, once a pull request has merged, the **delivery status**: one sentence and a line for each
Airfield (environment). For example:

> Merged, on dev since 14:02, not yet on prod.

or *Merged, not yet on any Airfield.* or *Merged, on dev since 14:02, on prod since 16:40.* Times are
in UTC.

### Where the fix is running

This is read from real deployments, never from what the item says. The merge records the commit that
carries the change. For each Airfield, Launchway looks at its successful deployments and asks whether
the commit the deployment ran **is that commit or a descendant of it** in the repository's history. If it
is, the fix is running there, since the earliest such deployment, and the line links the deployment, the
run and the version. If no deployment holds the commit, the Airfield is listed as not yet running it.
A deployment of an older commit does not count, and a revert or a Return to base (rollback) to an older commit stops it
counting.

If no Airfield is configured the answer says so rather than guessing. The status reads the list of
Airfields in the order they are set up; the `environments` setting in the [work
settings](../reference/work-format.md#settings) records which Airfields a level means to show, but today the view lists every
Airfield of the installation.

Items without a merged change show no delivery status: there is nothing to deliver yet.

You can also go the other way: find the items tied to a commit, a pull request or a branch with
`find_work_by_code`, `GET /api/v1/work/by-code`.

## Closing rules

Each level has a **close rule** in its settings. The default is: **when a pull request that names an item with a closing word is merged
by a person, the item moves to Done.**

| Rule `on` | Opening a pull request | Merging it |
|---|---|---|
| `merge` (default) | The item moves to In review | The item moves to the rule's state (Done by default) |
| `review` | The item moves to In review | The item stays in In review; a person finishes it |
| `never` | Links only | Links only |

These are the limits on the rule, all enforced by the server:

| Case | What happens |
|---|---|
| An item that is already done or won't do | Left alone |
| An item in another project | **Never closed unless the person who merged may change work there**; otherwise it is linked and the answer says "another project: you may not close its work" |
| Merged by an agent | **An agent's merge never finishes work.** The item moves to In review and waits for a person |
| A commit straight to the default branch | A person's commit with a closing word closes like a merge; an agent's is only linked |
| A pull request closed without merging | Nothing is closed; the link shows it was closed |
| The workflow has no such state | The first done state is used |

Closing across projects asks the **person's own** role: the person who merged needs a role that lets them
change work in the other project through a binding that names them. Bindings granted to a group do not yet
allow it (see [what is not built](#what-is-not-built)).

Every move made this way is in the item's history, as made by the person who merged, through Git.

## Release notes

`release_notes` lists the work finished between two moments, grouped by type: epics, stories, fixes
(bugs), issues and tasks, each as "title (KEY-12)". It leaves out items finished as Won't do and the
confidential items the caller may not see. The answer has the items, the refs by type and a Markdown text.

```text
launchway work release-notes --scope project:acme/shop --since 2026-11-01
```

```text
GET /api/v1/work/release-notes?scope=project:acme/shop&since=2026-11-01&until=2026-12-01
```

The MCP tool is `release_notes`. The same function is called by each, so a release can use it directly.

## What is not built

- Closing work in another project with a role granted to a **group** is not yet allowed; only a
  binding that names the person works.
- A failed delivery of an agent's change does not yet count toward
  [the pause rule](working-with-agents.md#pause); a revert does.
- The delivery status reads every Airfield of the installation; the work setting `environments`
  does not narrow it yet.
- Releases with tags, signed evidence and drafts are not part of work tracking; release notes are a list.
