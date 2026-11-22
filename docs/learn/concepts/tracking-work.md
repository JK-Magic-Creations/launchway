# Tracking work with people and agents

Work is the list of things to do: a bug to fix, a story to build, an epic that holds a dozen of them.
Launchway keeps it next to the code, so each item can show the pull request that carries it and where the
fix is running now. People and agents work from the same list, under the same rules.

## How it works

1. **Write the item.** An item has a type (issue, bug, story, task, epic or decision), a title, a description, a priority,
   labels, an estimate and perhaps acceptance criteria: the checklist that says what finished means. Items
   are named `PAY-12`: a prefix and a number.
2. **Plan it.** The backlog is one ordered list. A board shows the same items by state. A sprint takes
   some of them for a period, and you see what it committed to and what it finished. A limit on a board
   column is a warning, not a lock.
3. **Do it.** Name the item in a commit or a pull request ("fixes PAY-12"). The item shows the branch, the
   pull request and the Sorties (runs). Opening the pull request moves the item to In review.
4. **Finish it.** A person merges and the item moves to Done. A person always has the last word: an
   agent's merge never finishes anything.
5. **Follow it.** After the merge the item says where the fix is running, from the real deployments:
   "merged, on dev since 14:02, not yet on prod".

## Giving work to an agent

An agent is software with its own name and token. You register it for certain Flights (codebase) and kinds of work,
and then either assign it an item or give it a label to work from (a "pool"). The agent:

1. reads its Holding pattern (queue),
2. **claims** an item on a lease, which it keeps alive with heartbeats,
3. works, with a branch and a pull request,
4. **requests review**, or hands the item back with a reason.

A person reviews. The agent cannot approve its own change, cannot finish an item, cannot change its own
limits and cannot reach anything it was not registered for; the server checks all of it, whatever program is
on the other end. If an agent stops, the lease runs out and the item returns to the Holding pattern. You can switch an agent off
at any moment, and take its work over with the branch where it was left.

## How much an agent may do alone

A policy of the Flight says how far an agent goes without a person:

| Level | What happens |
|---|---|
| 0 | The agent only proposes |
| 1 | A person approves and merges. **The default** |
| 2 | Small changes to listed paths merge by themselves once every required check passed |

Levels 3 and 4 (launching to non-production and to production) can be written down but Launchway does
not act on them yet. Whatever the level, a change to a pipeline, to `.launchway/`, to an owners or
quarantine file, or one that deletes or skips tests always needs a person.

## Where to go next

- [Tracking work](../../guide/tracking-work.md): items, fields, links, search and the inbox.
- [Boards and sprints](../../guide/boards-and-sprints.md): planning and the charts.
- [Work and code](../../guide/work-and-code.md): keywords, closing rules and delivery.
- [Working with agents](../../guide/working-with-agents.md): registering agents, the policy and review.

Email is not available: you are told in the inbox and on Home, and other systems can listen with a webhook.
