# Working with agents

An **agent** is software that takes work from the same items people use, does it, and hands it back to a
person. Launchway treats an agent like a careful new colleague with a name tag: it has its own token,
may touch only the Projects and kinds of work it was registered for, holds an item only on a lease, and
cannot finish anything, approve anything or change its own limits. Whatever an agent does is marked as
agent-made in the item's history and in the [Logbook](../reference/events.md#audit-trail).

> [!IMPORTANT]
> **No particular agent product is integrated.** The interface is the product: a small set of HTTP
> routes, MCP tools and commands that any program can call with a token, and rules the server enforces
> whichever program is calling. Launchway does not run models, hold model keys or sit between an agent
> and its model; the agent brings its own. A small reference agent, [`docs-agent`](https://github.com/JK-Magic-Creations/launchway/tree/main/examples/agents/docs-agent),
> in `examples/agents` shows the whole loop with nothing but the command line and no model.

This page is for people who register agents and write policy, and for people who write agents.
For the general agent endpoint of Launchway (pipelines, runs) see [Agents (MCP)](agents.md); this page is
about work.

## Assigning work to an agent

There are two ways, and both end in the agent's [list of work](#the-loop):

- **Assign it.** Add the agent as an assignee, like a person. The server refuses an agent that is switched
  off, that is not registered for the item's Project, or that does not take the item's type.
- **Use a pool.** Give an agent a label it works from (for example `agent:docs`). Any open item with that
  label and **no assignee** is offered to it. The first agent to claim an item gets it.

An item is **not offered** to an agent when it is confidential, blocked by an open item, finished, held
by another agent on a live lease, outside the agent's Projects or types, of a difficulty its
[class](#class-routing) may not take, or in a level where agents are switched off. The Holding pattern (queue) keeps the
backlog order, with urgent work first.

## Registering an agent

A person who maintains the scope (admin of the Project, or of every Project of a Squadron, Wing or
Base) registers an agent. An operator cannot. From the command line:
`launchway work agents create docs --scope installation --project acme/shop --label agent:docs --type task`
(it prints the token once); `launchway work agents rotate-token NAME`, `disable NAME` and `enable NAME`
manage it, and `launchway work agents list` shows what each holds.

| Setting | Meaning |
|---|---|
| Name | 2 to 40 characters: letters, digits, `-` and `_`. Not the name of a person on any work |
| Scope | The level it is registered at |
| Projects | The Projects it may work in: patterns, never "all by default". `*` is allowed only for an agent registered at the Base |
| Types | The item types it takes (all if empty) |
| Labels | The pools it works from |
| Concurrency | Items it may hold at once (default 1, at most 20) |
| Per day | Items it may take in 24 hours (default 10, at most 500) |
| May merge | Whether it may merge its own work, where the [policy](#the-autonomy-policy) lets it |
| Model | A family name the registrant declares, such as the one the agent runs on |
| Class | `small`, `medium` or `large`: the size of job it takes |
| Budgets | Tokens and cents per day, and per item tokens, cents and seconds (0 is no limit) |
| Owner | The person accountable for it |

Registering shows the agent's **token once**: it starts with `lwa_` and only a hash is kept, so a lost
token cannot be read again. **Rotate** it to get a new one; the old one stops working at once. The owner
or a maintainer rotates, changes, switches off and deletes an agent. Every one of these is in the Logbook.

The agent presents the token as `Authorization: Bearer lwa_…`, to the HTTP routes under
`/api/v1/work/agent/…` and to the MCP server at `/mcp`. An agent's MCP session is offered only the tools an
agent may use (see [the loop](#the-loop)), never the tools for people.

## The loop

An agent works the same way every time:

In short: read the Holding pattern, claim an item on a lease, work on it (a branch, a pull request, comments),
heartbeat to keep the lease, then either request review or hand the item back with a reason. A person
reviews and finishes it.


| Step | HTTP route | MCP tool and command | What the server does |
|---|---|---|---|
| Who am I | `GET /api/v1/work/agent/me` | `get_agent_identity`, `launchway work agent me` | Says the agent's name, limits and what it holds |
| Read the Holding pattern | `GET /api/v1/work/agent/queue` | `get_work_queue`, `launchway work agent queue` | Lists what is waiting for it, never more than it may take |
| Claim | `POST /api/v1/work/agent/claim` | `claim_next_work`, `launchway work agent claim` | Gives it one item (the one named, or the next) on a **lease** of 30 minutes by default, 4 hours at most; moves a to-do item to In progress; adds the agent as assignee |
| Heartbeat | `POST /api/v1/work/agent/items/{ref}/heartbeat` | `heartbeat_work`, `launchway work agent heartbeat REF` | Extends the lease and keeps a note of progress |
| Report usage | `POST /api/v1/work/agent/items/{ref}/usage` | `report_work_usage`, `launchway work agent usage REF` | Counts the tokens, cents and seconds the agent says it used |
| Request review | `POST /api/v1/work/agent/items/{ref}/request-review` | `request_work_review`, `launchway work agent request-review REF` | Moves the item to In review, ends the lease, tells the people concerned |
| Hand back | `POST /api/v1/work/agent/items/{ref}/hand-back` | `hand_back_work`, `launchway work agent hand-back REF` | Returns the item to Ready with the reason, which is required |
| Review a pull request | `POST /api/v1/work/agent/pull-review` | `review_pull_request_as_agent`, `launchway work agent pull-review` | Stores a structured review (see [independent review](#independent-review-by-another-agent)) |

Besides these, an agent's MCP session is offered four item tools, for **the item it holds**: `get_work_item`,
`comment_on_work_item`, `update_work_item` (state between In progress, In review and Ready, labels, criteria) and
`link_work_code`. Over HTTP the same item routes accept its token, and it may attach a file (such as a
transcript) to the item it holds. It opens pull requests with its own token like any Git user; its pull requests are
marked as the agent's. The tools for people (creating, planning, registering agents, settings) are not offered to
an agent's token at all.

**A lease ends in one of four ways.** The agent requests review; the agent hands the item back; the lease
runs out because there was no heartbeat (the item goes back to Ready, the owner of the agent is told,
and the history says why); or a person takes the work over or switches the agent off. Two agents asking
for the same item cannot both get it: exactly one claim is stored.

An agent that is told its lease ran out simply claims again. An agent that has used its budget is
refused further claims; a person may raise the budget.

When an agent hands work over, the people who reported it or are assigned to it see it on Home and in the
inbox under "Agent review". A person then reads the pull request, and either approves and merges it, asks
for changes, takes the work over, or sends it back.

## What the server enforces

These rules hold whatever program holds the token, however it is written and whatever its settings say.
An agent cannot change any of them. Each is checked by the server before anything is stored, and
each has a test that fails if the rule is lost.

| Rule | Where it is proved |
|---|---|
| **Only a person marks work Done or Won't do.** An agent hands it over for review | `TestAgentLoopAssignClaimReviewDone` (the agent's move to done is refused, twice) and `TestAgentLoopThroughTheAPI` |
| **An agent changes only the work it holds**, on a live lease. Work it does not hold, or whose lease has run out, is refused | `TestAgentLoopAssignClaimReviewDone` |
| **An agent does not change who is assigned, or how work is planned.** Assignees, priority, estimate, type, parent, milestone, sprint, dates, difficulty and confidentiality are a person's; it says what it wants in a comment | `TestAgentLoopAssignClaimReviewDone` (reassigning is refused) |
| **An agent moves work only to In progress, In review or Ready** | `TestAgentLoopAssignClaimReviewDone` |
| **Agents do not reorder the backlog, and do not start or complete sprints** | in code (`Rank`, `StartSprint`, `CompleteSprint`); no separate test yet |
| **An agent never approves a pull request through the ordinary review, never merges unless it is allowed to, and never turns on auto-merge** | `TestAgentLoopThroughTheAPI` (each call is refused) |
| **A pull request by an agent is not merged without a person's approval**, whoever presses merge, unless the [policy](#the-autonomy-policy) lets it through or an [independent agent's review](#independent-review-by-another-agent) counts | `TestAgentLoopThroughTheAPI` ("needs a person's approval") and `TestAutonomyGate` |
| **An agent's merge never finishes work.** The item waits in review for a person | `TestPullMergeMovesWorkAndNeverClosesAcrossProjects` |
| **An agent never closes work in another Project**, and nobody does without the right to change work there | `TestPullMergeMovesWorkAndNeverClosesAcrossProjects` (for a person without the right); for agents in code (`mayCloseFor`) |
| **An agent works only in the Projects it was registered for**, for work and for Git. A claim, an assignment or a push elsewhere is refused | `TestKillSwitchBudgetsAndScopeLimits` and `TestAgentLoopThroughTheAPI` (a push to another repository is refused) |
| **An agent is never offered, and cannot claim, a confidential or blocked item** | in code (the Holding pattern and claim checks); no separate test yet. `TestConfidentialAndCrossProjectVisibility` proves confidential items are hidden from people who may not see them |
| **An agent cannot exceed its limits**: items held, items per day, its daily budget | `TestAgentLoopAssignClaimReviewDone` (the second claim is refused) and `TestKillSwitchBudgetsAndScopeLimits` |
| **Only a person who maintains the scope registers an agent**; an operator cannot | `TestAgentLoopThroughTheAPI` (403) and `TestKillSwitchBudgetsAndScopeLimits` |
| **Switching an agent off takes back everything it holds at once**, and its token stops working on the next request | `TestKillSwitchBudgetsAndScopeLimits` and `TestAgentLoopThroughTheAPI` (401) |
| **A level's Master switch (kill switch) stops every agent there**, and a level above that is off keeps it off below | `TestLevelKillSwitchAndTakeOver` |
| **An agent comments on and attaches files to only the work it holds** | in code (`AddComment`, `AddAttachment`); the test proves it may on work it holds |
| **Everything an agent does is marked as made by an agent** in the history | `TestAgentLoopAssignClaimReviewDone` and `TestAgentLoopThroughTheAPI` |
| **Changes to pipeline files, `.launchway/`, owners files, quarantine files, and deleting or skipping tests never merge without a person**, at any autonomy level | `TestAutonomyGate` |
| **Autonomy above "a person approves and merges" needs a recorded reason**, and level 2 lists the paths it covers | `TestAutonomyGate` |
| **Levels 3 and 4 are enforced as level 2** | `TestAutonomyGate` |
| **An agent never reviews work it did**, nor a pull request it opened or pushed to, nor one for an item it held | `TestAgentReviewIndependenceAndPolicy` |

## The autonomy policy

The policy says how far an agent's work may go without a person. It is set per level, like the other
work settings (see the [work format reference](../reference/work-format.md#agent-rules)), by a person
who maintains it.

| Level | Name | Behavior |
|---|---|---|
| 0 | Propose only | Agents open no pull requests; they comment and hand back |
| 1 | A person approves and merges | **The default.** An agent's pull request merges after a person approves it and the checks pass |
| 2 | Auto-merge for listed changes | A pull request may merge **without a person** when every required check passed (and at least one is required), the change is only in the listed `paths`, its item is of a listed `type` (any if none), it is within `max_files` (10) and `max_lines` (200), and autonomy is not paused |
| 3 | Launch to non-production | **Not available.** It can be written down, but the server does not act on it and enforces level 2 |
| 4 | Launch to production | **Not available.** The same: enforced as level 2 |

A level above 1 must carry a reason, which is recorded, and level 2 must list the paths it covers.
Anything not listed needs a person.

Whatever the level, these **always need a person**: pipeline files, everything under `.launchway/`
(branch rules, standards, security settings, the autonomy policy), owners files, quarantine files, a
change that deletes a test, and a change that adds a skipped test. A failing or skipped required check
blocks every merge. If no check is required on the branch, level 2 cannot auto-merge, because nothing would show
the change is safe.

At level 2 the person-free merge is made by an agent that has "may merge" (and only for a change the
gate lets through), or by auto-merge a person set on the pull request. There is no separate background
job that merges agents' work; the existing auto-merge sweep is what acts.

### Where the effective policy comes from

Settings are read from the nearest level that sets them: Project, then its Squadron, then the Wing, then
the Base. The **autonomy** setting of a nearer level replaces the farther one's. The page of the Project shows the level
in force and **where it comes from** ("installation", "project:acme/shop", or "default"), and, if it is above
what the server acts on, says so. A level cannot loosen what a farther level switched off: if agents are
off at the Base they are off below.

### Pause

Autonomy **pauses by itself** when an agent's changes go wrong: when `pause_after` (3 by default) of a
Project's recent agent changes were reverted within `pause_days` (14), the Project stops auto-merging
until a person clears the pause with a reason ("cause found and fixed"). While paused, everything still
works at level 1: it needs a person. Only a person who maintains the Project clears a pause
(`clear_autonomy_pause`, `POST /api/v1/work/autonomy/clear-pause`). Today the server records a revert (a
pull request that reverts an agent's merged change); the rule also counts a failed delivery, but nothing
writes that outcome yet.

### The Master switches {#the-kill-switches}

| Switch | Effect | Who |
|---|---|---|
| An agent's **disabled** flag | Its token stops working at once; everything it holds returns to Ready | The agent's owner, or a maintainer of its scope |
| A level's **agents off** (`agents.enabled: false`) | No agent can claim or take work there, and none is offered it; every agent pull request there needs a person | A maintainer of that level |
| Autonomy level 0 or 1 | No automatic merge | A maintainer of the level |
| Pause | No automatic merge until a person clears it | Automatic; cleared by a maintainer |

## Independent review by another agent

Normally an agent's review is **advice**: it shows on the pull request, marked as advice, with why it
does not count. A Wing or a Project that wants a second pair of eyes before a person looks can let a
**different agent's** approval count. That is a policy of the level, off by default:

| Setting | Meaning |
|---|---|
| `mode: human_only` | The default. An agent's review is advice only |
| `mode: independent_agent_allowed` | An independent agent's approval counts for pull requests that fit `paths`, `types`, `max_files` and `max_lines`. It needs a recorded `reason` and the paths |
| `require_different_model` | The reviewer must declare a model different from every author agent's, and the author must declare one |
| `sample_percent` | This share of merges an agent approved is flagged for a person to look at afterwards |

An agent's review is a **structured verdict**: a decision (`approve`, `request_changes` or `comment`),
the list of the criteria it checked, each with what it found and the evidence, the risks it sees, whether
the change stays in scope, a confidence from 0 to 1, and a summary. A verdict without criteria and evidence is refused.

A review **counts as an approval** only when all of these hold, and otherwise it is stored as advice
with each reason:

1. It is an approval, the reviewer judged the change in scope, and agents are on at this level.
2. The reviewer is a registered agent that did not open, push to or author the pull request, and **never
   held or was assigned the work** (by lease or by earlier claim). It is refused outright if it did.
3. When the policy asks for a different model, the reviewer's declared model differs from the author's
   (an agent that declares none does not count).
4. The change is within the listed paths and the size limits, and of a listed type.
5. It touches nothing on the always-a-person list, deletes no test, skips no test.

A request for changes by any agent withdraws every agent approval. A person's own approval of course still counts as before,
and a person remains able to approve anything.

**Human audit sampling.** When a merge that an agent's approval counted for happens, the server flags it
for a person to look at afterwards with the policy's probability; asking twice gives the same answer for
the same pull request. A maintainer sees the list of flagged merges (`list_work_samples`) and records
that they looked (`complete_work_sample`). The reviews of a pull request are listed with
`list_work_pull_reviews`.

## Budgets and the spend view

An agent reports what it used on an item (`report_work_usage`): tokens, cents and seconds. Launchway
**counts what is reported**; it does not meter models, so the numbers are only as true as the agent is.
When an item's budget or the agent's daily budget is passed, the answer to the report says so and
**further claims are refused**; nothing is stopped part-way, and a person may raise the budget.

The **spend view** (`get_work_spend`, `GET /api/v1/work/spend`) shows, for what you may see, each agent's use over
the last 24 hours against its daily budget, each Project's use over all time, and the costliest items with
what is left of their budget.

## Taking over

A person can take work from an agent at any time (`take_over_work_item`, `POST
/api/v1/work/items/{ref}/take-over`): the person becomes the assignee, the agent's lease ends, the agent
stops being an assignee, and **the agent's branch is left where it is** for the person to continue. The
agent's next heartbeat is refused, which tells it to stop. The history records who took over.

## Class routing

Not every agent should take every job. Each item can have a **difficulty** (`small`, `medium`, `large`),
set by a person or by **rules** of the level (a label such as `trivial` gives `small`; an estimate of at
least 8 gives `large`). Each agent declares a **class**. A level's `classes` setting says which classes
may take each difficulty, for example `large: [large]` and `small: [small, medium, large]`. A claim by an
agent whose class does not fit is refused and says why; such items are left out of its Holding pattern. A
difficulty with no entry is open to every class.

## Reading what agents did

Each agent has a log of its recent actions (`get_work_agent_actions`): claims, heartbeats, hand-backs,
reviews, token rotations, being taken over. Items carry the same facts in their history. Every change a
person makes to an agent or to the policy is a Logbook entry (`work.agent_create`, `work.agent_update`,
`work.agent_rotate`, `work.agent_delete`, `work.settings`, `work.autonomy_clear_pause`).

## What is not built

- Autonomy levels 3 and 4 (launching to non-production Airfields (environments) and to production by agents) are
  stored but not acted on.
- The model gateway: Launchway does not call models for agents or meter them; budgets are as reported.
- A failed delivery does not yet count toward the pause rule.
- Auto-merge has no trigger of its own beyond the existing auto-merge sweep.
