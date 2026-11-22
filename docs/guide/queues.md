# Queues and limits

A busy day should make runs wait, not fail. This page says what Launchway does when there is no
room for a step, and why a Sortie (run) or step that is not moving says so in plain words.

## A step that has no room waits

A step can fail to start for lack of room that will come back: the step namespace's Kubernetes
quota is used up by other Sorties, or no node has space for its pod. Launchway does **not** fail the
step. It stays *queued*, the Sortie page, the API (`waiting_for` and `waiting_code` on the step), the
command line (`launchway run show`) and MCP say what it waits for, and it is tried again after a
pause that grows from 10 seconds to a minute:

> waiting for room in the step namespace: this Sortie's steps build hold 1 CPU across 1 pod, and
> release it when they end; the namespace allows 8 CPU and 7.8 CPU is reserved in all; this pod
> needs 0.5 CPU more, so 0.3 CPU must be released first. This is what is reserved by the pods, not
> what a step uses: nothing ran out of memory; held by runs #43–#50; this run is next after #52

The sentence names the shortage, the Sorties that hold the room, and where this run is in line. Each
reason has a stable code beside the sentence, for scripts:

| `waiting_code` | Meaning |
|---|---|
| `quota_full` | The step namespace's quota has no room for the step's pod |
| `cluster_full` | No node has room for the step's pod |
| `no_runner` | No Station (runner) that may take the step has been seen lately |
| `runners_busy` | Every Station that may take the step is busy |
| `retry_pause` | A retry waits out its pause |

On Kubernetes each step has a pod of its own, which holds quota only while the step runs, so the sentence is
about one step's pod: what it needs (its own container, the helper that holds the workspace and the services), and what is
reserved by other pods. When the holders include steps of the **same** Sortie it names them: they are running and release
the room when they end, so the wait is short and cannot be a deadlock. A pod of the Sortie's own that no step runs in (an
attempt whose lease was lost) is never waited for: the Station removes it before it makes the step's pod. See
[One pod for each step](../reference/runner-config.md#one-pod-for-each-step).

A wait is not a failed attempt: the step runs as the same attempt, `retry` does not count it, and
the Sortie's timeout does not stop counting, so a run does not wait for ever.

### The longest wait

A step waits for room for at most `queues.max_wait` (default **60 minutes**, in the
[server settings](../reference/server-config.md#queues-and-limits); a Flight (codebase) may set its own).
After that it fails with the reason and what to change: free up room by canceling runs that are not
needed, or give the step namespace more quota, or let steps wait longer. `off` waits for ever.

### Steps that can never start fail at once

Waiting only helps when room can come back. A single step that asks for more than the **whole**
quota (for example 10 CPU in a namespace that allows 8) never can, so it fails at once with both
numbers and what to change, instead of waiting for an hour. On Kubernetes the check is made for the step's pod
alone: the step's own container, the helper and the services, not the Sortie's other steps.

## Limits on concurrent Sorties {#limits-on-concurrent-runs}

A burst of eight dependency pull requests used to start eight runs at once, fill the step
namespace's quota, and starve the main build. Now a run over a limit is **held**: it is created, it
shows up in Activity under *Waiting for room*, and none of its steps is queued until room is free.
Nothing is configured for this; these are the defaults, and each is a setting in the
[server settings](../reference/server-config.md#queues-and-limits) that a Flight may override:

| Limit | Default | Setting |
|---|---|---|
| Runs of a Flight at once | 10 | `queues.runs` |
| Pull-request runs of a Flight at once | 4 | `queues.pull_request_runs` |
| Runs started by automation accounts at once | 2 | `queues.automation_runs` |
| Runs of one pipeline, of branch pushes, of schedules, of manual Sorties | no limit | `queues.pipeline_runs`, `push_runs`, `schedule_runs`, `manual_runs` |

`0` means no limit. Pushes to protected branches (the Flight's default branch) are **never
held**, and a Sortie started by hand is not held by the Flight total. Running steps are never
stopped to make room.

A held run says why, with a stable code beside the sentence:

> waiting for room: this Flight allows 2 Sorties started by automation accounts at once, and 2 are
> going (runs #1–#2); 1 held run go first

| `code` | Meaning |
|---|---|
| `automation_limit` | The automation limit is reached |
| `kind_limit` | The limit for this kind of run is reached |
| `pipeline_limit` | The pipeline's limit is reached |
| `project_limit` | The Flight's total is reached |
| `held_by_operator` | A person held it |

### The order

When room frees up, held runs go in this order: a run an operator moved to the front, then by
priority (runs started by hand, then Patrols (scheduled Sorties (runs)) and branch pushes, then pull requests, and
automation last), then the oldest. So a queued push goes before queued pull requests.

### Newer commits replace older Sorties {#newer-commits-replace-older-runs}

A pull request gets a new run when it gets a new commit. By default the older run of the **same**
pull request is canceled, with the reason "superseded by run #N: a newer commit was pushed to the
same pull request". Turn it off with `queues.supersede_pull_requests: false`, or per pipeline with
`supersede: false`. A Sortie that holds an open deployment is never canceled this way.

### Run next, hold, release

An operator can change the order, in the Sortie's own actions, in Activity, with `launchway runs queue
next|hold|release <run>`, the API (`POST /api/v1/runs/{id}/queue`) and the MCP tools `list_queue` and
`queue_run`:

- **Run next** puts the run at the front, and lets it go now if the limits have room.
- **Hold** keeps it waiting, even when there is room, until someone releases it.
- **Release** lets the limits decide again.

The Sortie's `actions` list says which are available now, and why not when one is not.
`launchway runs queue` and `GET /api/v1/queue` list the Holding pattern (queue) in order with each Sortie's reason and
the runs it waits behind.
