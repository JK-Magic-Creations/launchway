# Work queries

The query language of work tracking. One line of terms separated by spaces; every term must match. The same
line is used by the search box, saved views, boards, the list route and the `list_work_items` tool.
See [tracking work](../guide/tracking-work.md#finding-work).

```text
state:in_progress type:bug -label:wontfix assignee:me updated:<7d "payment fails"
```

You only ever get items you may see: confidential items you may not read and Flights (codebase) you may not read are left out
whatever the query says.

## Terms

| Term | Matches |
|---|---|
| `word` or `"some words"` | The text in the title or description, ignoring case. Words are searched as written, not split into stems |
| `state:in_progress` | The state with that key. `state:open` is every state that is not finished; `state:closed` is done or cancelled |
| `type:bug` | The item type: `issue`, `bug`, `story`, `task`, `epic` |
| `priority:high` | `urgent`, `high`, `normal` or `low` |
| `label:ui` | Has the label (labels are lower case) |
| `assignee:ada`, `assignee:@ada`, `assignee:me`, `assignee:none` | Assigned to that person or agent; to you; or to nobody. `agent:` is the same as `assignee:` |
| `reporter:ada`, `reporter:me` | Raised by that person |
| `milestone:"Launch"` | In the milestone with that title |
| `sprint:"Sprint 4"`, `sprint:none` | In the sprint with that name; or in none |
| `parent:PAY-3`, `epic:PAY-3` | Children of the item, and their children |
| `repo:acme/shop` | Work of a repository: in its own scope, or tied to its code (a branch, pull request, commit, run, or the repository itself) |
| `ref:PAY-12` | The item with that reference |
| `key:S01-01` | The item imported with that key |
| `is:open`, `is:closed` | Not finished; finished (done or won't do) |
| `is:blocked` | Blocked by an item that is not finished |
| `is:unassigned` | Has no assignee |
| `is:claimed` | Held by an agent on a live lease |
| `is:confidential` | Is confidential (and you may see it) |
| `is:overdue` | Past its due date and not finished |
| `is:epic` | Is an epic |
| `updated:`, `created:`, `due:`, `closed:` | Dates, see below |

A minus before a term turns it around: `-label:wontfix`, `-is:blocked`, `-"not this"`.

A word with a colon whose first part is not one of the names above is searched as text, so `error:disk`
looks for the characters `error:disk`. A term with a known name needs a value: `label:` alone is an error,
as is a value `is:` does not take.

A query has at most 30 terms.

## Dates

`updated`, `created`, `due` and `closed` take:

| Value | Meaning |
|---|---|
| `2026-01-31` | That day (UTC) |
| `>2026-01-01`, `<2026-01-01` | After that date; before it |
| `today` | Today. `due:<today` is every item due before today |
| `7d`, `2w` | An age: days or weeks ago |
| `<7d` | **More recent** than 7 days ago: within the last week |
| `>30d` | **Older** than 30 days |

The arrows point along the clock for dates and along the age for ages: `updated:<7d` finds work touched this week, and `created:>30d`
finds work older than a month. Items with no value for the date (no due date) never match a date term.

## Sorting and paging

| Parameter | Values |
|---|---|
| `sort` | `rank` (the backlog order, default), `updated`, `created`, `priority`, `due` (items with no date last), `number` |
| `desc` | `true` reverses the order |
| `limit`, `offset` | 50 by default, 500 at most |
| `parent` | Only the direct children of this item |
| `scope` | `project:owner/name`, `unit:name`, `organization` or `installation`; leave out for every scope you may read |

Equal items are ordered by number. A page says the total, so a client can page through.

## Saved views

A **view** is a name and a query in a scope, private or shared. `POST /api/v1/work/views` with
`{"scope", "name", "query", "shared"}` saves one, `GET /api/v1/work/views?scope=…` lists the ones you may
use, and `DELETE /api/v1/work/views/{id}` removes one you own. MCP: `save_work_view`, `list_work_views`,
`delete_work_view`. A view stores only the query, so it always shows what you may see now.

## Examples

| To find | Query |
|---|---|
| My open work, most urgent first | `assignee:me is:open` with `sort=priority` |
| Bugs nobody has taken | `type:bug is:unassigned is:open` |
| What an agent holds right now | `is:claimed` |
| What waits for a person after an agent handed it over | `state:in_review is:open` |
| Overdue work | `is:overdue` |
| Finished in the last two weeks | `is:closed closed:<2w` |
| Stale work | `is:open updated:>30d` |
| Everything under an epic | `epic:PAY-3` |
| What is blocking the release | `milestone:"Release 2" is:blocked` |
