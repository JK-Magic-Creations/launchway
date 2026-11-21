# Events

Every event the server receives is a record, and so is every decision made about it. When a run did not start,
or started when you did not expect it, the event log says why, in words.

## What an event is

| Source | What happened |
|---|---|
| `push` | A branch moved or a tag was created |
| `pull_request` | A pull request was opened or its head moved |
| `schedule` | A schedule fired |
| `webhook` | A webhook trigger was called |
| `event_source` | An event source of the project received a call (see [Event sources](event-sources.md)) |
| `manual` | A person, a token or an agent started a run, or re-ran one |
| `run_result` | Another project's run ended (see [Triggers on results](result-triggers.md)) |
| `launch_result` | Another project's launch ended (see [Triggers on results](result-triggers.md)) |

Each event records when it arrived, whether its sender was **verified** (a signed webhook is; a call with a wrong
signature is recorded too, as not verified), a one-line summary, the ref and commit it was about, and a small
**payload**. The payload is kept as received with the value of every key that looks like a credential (`token`,
`secret`, `password`, `authorization`, `api_key`, `signature`, `cookie` and similar) replaced by `[removed]`, and cut
at 16 KiB. Do not put a secret in a payload on purpose: the removal is a safety net, not a promise.

## Decisions

An event is judged against each trigger that could start a run. Each judgment is a **decision** with a sentence
saying why:

| Decision | Meaning | Example reason |
|---|---|---|
| `started` | A run was created | `started run #41` |
| `held` | A run was created and waits | `run #42 was created and waits: waiting for lane release (run #41 is running)` |
| `waiting` | Not decided yet: a window is open, or the Git host has not said which branch a pull request targets | `waiting for 3 more seconds without a new event` |
| `filtered` | The trigger's rules say this starts nothing | `paths: only docs/** changed` |
| `superseded` | A newer event took its place | `replaced in lane release by run #43` |
| `skipped` | The lane was busy and says `when_busy: skip` | `skipped: lane release is busy (run #41)` |
| `duplicate` | The dedupe key was seen before | `the key order-7 was seen in event evt_…` |
| `refused` | Not verified, over a limit, too large or not allowed | `the signature did not match` |
| `error` | The pipeline or the request has problems | `the pipeline file has problems` |

A start is written in the same transaction as the run it started, so an event that says `started` always has its
run, and a run started by anything has an event. An event that was refused or filtered is written on its own.
The guarantee for every event and trigger is **one start, or one recorded reason**, however many servers run.

## Reading the log

The **Events** page under Activity lists events newest first with filters (source, decision, verified, project) and
opens one to show its payload, its decisions and the runs it started. From the command line:

```sh
launchway events list --project payments --decision filtered
launchway events show evt_0f3a...
```

You see the events of the projects you may read. The same data is `GET /api/v1/events` and the MCP tool `list_events`.
The newest 5000 events of each project are kept, and only the newest 200 that were not verified (anyone can send those).

## Replay

An operator of a project can **replay** an event: the same run is started again, at the same commit and with the same
inputs, as a new event that points back to the first. Replaying is for "start what that event should have started"
after you fixed the cause. It is a manual start in every other way: the pipeline's lane and limits apply, and the
Logbook records `event.replay` with your name and the event.

An event whose sender was not verified cannot be replayed, and neither can one that has nothing to repeat. The page
says which, and what to do instead.

```sh
launchway events replay evt_0f3a...
```
