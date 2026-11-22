# Boards and sprints

Planning in Launchway is a view of the same items, not a second copy. A board is the items of a project
drawn by state; the backlog is their order; a sprint is a period with a goal and a set of items; the
charts are computed from the history of the items. Nothing here makes anyone's decision for them: every
limit and every piece of capacity advice is a **warning**, never a refusal.

For the items themselves see [tracking work](tracking-work.md).

## Boards

A board has a **column for each state** of the workflow, in order, with the items in backlog order. Each
column shows how many items it holds, their points, and how many are shown (up to 100 a column by default,
200 at most). A board can be narrowed with a [query](../reference/work-queries.md) and limited to one
sprint, or to the active sprint.

### Swimlanes

Cut the board into horizontal **lanes** by assignee, by epic or by priority. Each card is in the lane of
its assignee (or of its first assignee), its epic or its priority; items with none are in a lane of their
own. Lanes change nothing about the items.

### Work-in-progress limits

A state can have a limit of cards. When a column holds more, the board marks it **over its limit** and
says by how much. That is all it does: **you can still move a card into a full column**, and an agent can
still claim work into it. A limit is a prompt to finish something first, not a lock. Give the limit with
the state in the level's [settings](../reference/work-format.md#settings).

### Moving cards: drag, keyboard and menu

Dragging is a convenience. Every move can also be made from the keyboard alone, through the **Move to…**
menu on each card, and what was done is announced for screen readers ("Moved PAY-12 to In review."):

| Way | How |
|---|---|
| Drag | Drag the card to a column, and to a place among the cards of the column |
| Menu and keyboard | Tab to the card's **Move to…** button, open it with Enter, choose a column (the card's own is marked "here") or **Move up** or **Move down** with the arrow keys and Enter, and close it with Escape |
| Command, API, MCP | `launchway work items move PAY-12 in_review`, `POST /api/v1/work/items/{ref}/move`, or `move_work_item`, optionally "before" another item |

All of them make the same call: the item goes to the state, and optionally before another item. The rules of the
workflow apply to every one of them, so a card cannot be moved to Done by an agent however it is asked,
and a person's move is recorded in the item's history like any other change.
See [accessibility](accessibility.md) for what the interface supports.

## The backlog and its order

Items in a scope have one **order**. New work goes to the bottom. Put an item at the top, at the bottom,
or before or after another item of the same project (`rank_work_item`, `launchway work items rank PAY-12 top`, `POST /api/v1/work/items/{ref}/rank`). On the backlog page each row has a **Move…** menu with the same choices: to the top, the bottom, up, down or before another item, and a control to put the item in a sprint. Moving one item
changes one number, not the whole list, so two people reordering at once line up rather than overwrite
each other. Agents do not reorder the backlog.

The board, the backlog list and the Holding pattern (queue) an agent reads all use this order (the agent's list of work puts urgent
work first and keeps the order within a priority).

## Milestones

A **milestone** is a goal with a due date, such as a release. An item belongs to at most one. A milestone
shows how many of its items are done, the points, and a percentage, and is open or closed. Milestones are
kept per scope: a Squadron, a Wing or the Base may have their own for work that spans projects.

## Epics and the roadmap

An **epic** groups stories, issues and bugs. Its progress is how many of its children are done. The
**roadmap** lists, in one view, every epic, milestone and sprint of a scope with its dates and its
progress:

| Row | Dates | Progress |
|---|---|---|
| Epic | Its own start and due dates; when it has none, the earliest start and latest due date of its children | Children done out of children, as a percentage |
| Milestone | Its due date | Items done out of items |
| Sprint | Its start and end | Items done out of items |

The roadmap is drawn as bars on a time line, **and always also as a table** with the same rows, dates and
percentages (title, kind, start, due, progress, state), so that nothing depends on seeing a drawing.
`get_work_roadmap` and `GET /api/v1/work/roadmap` return the rows.

## Sprints

A **sprint** has a name, a goal, a start and an end date, and a **capacity**: for each person, a number
of points (or hours) they can give. A sprint is `planned`, `active` or `completed`. Only one sprint of a
scope is active.

1. **Plan.** Create the sprint and put items in it. Estimates may still change. The sprint shows what it
   holds and the capacity.
2. **Start.** Starting records what the sprint **committed to**: each item with its estimate at that moment.
3. **During.** Items may join later; they count as **added** after the start, not as part of the
   commitment. Estimates may change; the commitment does not.
4. **Complete.** Items that are done are recorded as **completed**. Items that are not done **carry over**:
   you name the next sprint, or send them back to the backlog. Each carried item records where it came
   from in its history, and the finished sprint keeps its numbers.

Only people (not agents) start and complete a sprint.

### Commitment, completed and carry-over

| Number | Meaning |
|---|---|
| Committed | The estimates of the items in the sprint when it started |
| Added | The estimates of items that joined after it started |
| Completed | The estimates of the items finished by the time it ended |
| Carried over | How many items were not done and moved on |

Because the commitment is a snapshot, a later change of an estimate does not rewrite how the sprint went.

### Capacity advice

When the committed work plus the added work is more than the sum of the capacities, the sprint shows a
**warning**: *the sprint holds more work than the capacity; taking something out, or raising a capacity,
are both fine; nothing is changed for you.* Launchway advises and never removes, resizes or reorders anything. The
capacity of each person is at least 0, for up to 200 people.

## Charts

All charts are computed on request from the record of what happened to each item (when it joined a
sprint, when it entered each state, when it finished), not from stored totals. So they can be explained,
and a correction to the record corrects the chart. They show only what you may see: confidential items
are left out unless you maintain the scope.

| Chart | What it shows | How it is computed |
|---|---|---|
| Burndown | The work left in a sprint, day by day, against a straight line | For each day of the sprint, the estimates of the items in the sprint that were not yet done at the end of that day. The straight line goes from the commitment on the first day to zero on the last. Items added later are shown as added, and do not move the line. |
| Velocity | How much each recent sprint completed | For the last 8 completed sprints (up to 50), the committed and completed estimates, and the average completed |
| Cumulative flow | How many items were in each state, each day | For each day of the last 30 days (up to 365), each item is counted in the state it was in at the end of that day, read from its history |
| Lead time | How long work takes from asking to done | For items finished in the period (90 days by default, up to 730): from the item's creation to its finish. The median, the 85th percentile and the mean, in days |
| Cycle time | How long work takes once started | The same items, from the first time the item entered a doing or review state to its finish. A histogram groups items into under 1 day, 1 to 2 days, 2 to 4 days, 4 to 7 days, 1 to 2 weeks, 2 to 4 weeks and over 4 weeks |

Cycle and lead time use the 85th percentile as well as the median, because most work takes about the
median and a plan should allow for the slower one in seven.

Every chart is also given as a table of numbers beside the drawing, and through the API
(`GET /api/v1/work/sprints/{id}/burndown`, `/charts/velocity`, `/charts/flow`, `/charts/times`) and the
MCP tools `get_work_burndown` and `get_work_chart`.

## Where planning settings live

| Setting | At which level |
|---|---|
| Estimate unit (points or hours) | Any level; the nearest wins |
| States, limits and transitions | Any level; a nearer level replaces the whole workflow |
| Sprints, milestones | Each scope has its own |
| Agent rules | See [working with agents](working-with-agents.md#the-autonomy-policy) |

Settings are read from the nearest level that sets them: the Project, its Squadron, the Wing, then the
Base. They are described in the [work format reference](../reference/work-format.md#settings).
