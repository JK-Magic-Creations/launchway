# Queues and limits

A busy day should make runs wait, not fail. This page says what Launchway does when there is no
room for a step, and why a run or step that is not moving says so in plain words.

## A step that has no room waits

A step can fail to start for lack of room that will come back: the step namespace's Kubernetes
quota is used up by other runs, or no node has space for its pod. Launchway does **not** fail the
step. It stays *queued*, the run page, the API (`waiting_for` and `waiting_code` on the step), the
command line (`launchway run show`) and MCP say what it waits for, and it is tried again after a
pause that grows from 10 seconds to a minute:

> waiting for room in the step namespace: 7.8 CPU of 8 CPU already requested, and this step needs
> 0.5 CPU more; held by runs #43–#50; this run is next after #52

The sentence names the shortage, the runs that hold the room, and where this run is in line. Each
reason has a stable code beside the sentence, for scripts:

| `waiting_code` | Meaning |
|---|---|
| `quota_full` | The step namespace's quota has no room for the step's pod |
| `cluster_full` | No node has room for the step's pod |
| `no_runner` | No runner that may take the step has been seen lately |
| `runners_busy` | Every runner that may take the step is busy |
| `retry_pause` | A retry waits out its pause |

A wait is not a failed attempt: the step runs as the same attempt, `retry` does not count it, and
the run's timeout does not stop counting, so a run does not wait for ever.

### The longest wait

A step waits for room for at most `queues.max_wait` (default **60 minutes**, in the
[server settings](../reference/server-config.md#queues-and-limits); a project may set its own).
After that it fails with the reason and what to change: free up room by canceling runs that are not
needed, or give the step namespace more quota, or let steps wait longer. `off` waits for ever.

### Steps that can never start fail at once

Waiting only helps when room can come back. A single step that asks for more than the **whole**
quota (for example 10 CPU in a namespace that allows 8) never can, so it fails at once with both
numbers and what to change, instead of waiting for an hour.
