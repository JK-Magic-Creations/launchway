# Triggers on results

Start a run of one project when a run, or a launch, of another project ends: build a library, then test the
application that uses it; finish a deployment, then run the acceptance tests of another team. Every start is an
[event](events.md) with its reason, and every refusal is too.

## Two decisions, by two sets of people

The **upstream** project (the one whose result is waited for) decides who may trigger on it, because its results
are its own:

```sh
launchway run-triggers allow library app            # operators of "library"
launchway run-triggers allow library app --share-outputs
```

The **downstream** project (the one that starts) says what it waits for:

```sh
launchway run-triggers create app --name after-library --on library --when succeeded \
  --pipeline release --filter 'run.branch == "main"' \
  --map library_version=run.sha --map build_url=run.url
```

A trigger is refused when the upstream project has not allowed the downstream one (the message names the command to
run), when you cannot read the upstream project, and when it would make a cycle (A triggers B triggers A, directly or
through other projects). If the permission is withdrawn later the trigger stays and is refused, with that reason,
at each result, until it is given again.

## What starts it

- `--when` for a run: `succeeded` (the default), `failed`, `canceled` or `any`. For a launch (`--launch`, a
  deployment that ended): `deployed` (the default), `failed`, `rolled_back` or `any`.
- `--pipeline NAME` limits it to results of the upstream pipeline with that name.
- `--filter` is an [expression](event-sources.md#expressions) over `run`, `launch` and `outputs` that must be true.
- Only results that end **after** the trigger is made count; history starts nothing.

## What crosses

Only a description of the result, by reference. In expressions:

| Name | Holds |
|---|---|
| `run.id`, `run.number`, `run.project`, `run.pipeline`, `run.status` | which run |
| `run.ref`, `run.branch`, `run.tag`, `run.sha`, `run.event` | what it ran at |
| `run.url` | its address in this installation, `/runs/<id>` |
| `launch.verdict`, `launch.environment`, `launch.service`, `launch.version` | for a launch |
| `outputs.<step>.<name>` | the outputs steps wrote, **only** if the upstream allowed `--share-outputs` |

No secret, log, file or artifact content crosses; the downstream pipeline fetches what it needs by the reference,
with its own permissions. Inputs are checked against the downstream pipeline's inputs like a person's.

## What you get

The run has event `manual`, and its actor is `result:<upstream>#<number>`. On the downstream project's
[Events](events.md) page the event reads "Run #12 of library ended: succeeded", with the decision
(`started run #5`, or why not: `filtered`, `refused`, `error`). Each result is judged once, however many servers run.
