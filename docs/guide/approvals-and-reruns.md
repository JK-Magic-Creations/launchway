# Approvals and re-runs

Pausing a run for a person to decide, re-running from any step, and the record of what each step was given.

## Approvals

An approval step pauses the run until a person decides:

```yaml
steps:
  approve-prod:
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy 2.14 to production?
    timeout: 24h
  deploy-prod:
    image: alpine:3.22
    needs: approve-prod
    run: ./deploy.sh
```

An approval takes `needs`, `when`, `message` and `timeout`, and runs nothing, so it has no image or
script. When its needs have succeeded it waits, and every pending approval is listed on the
**Approvals** page. Approving lets the steps after it run. Rejecting, or nobody deciding before the
timeout, fails it: the steps after it are skipped, and resuming the run asks again. Each decision
is kept with the run: who made it, when, and their comment.

Decide in the web UI, or over the API with `POST /api/v1/runs/{id}/steps/{step}/approve` (or
`/reject`) and an optional `{"comment": "..."}`. `launchway run` asks in the terminal, and rejects
the approval if there is no terminal to ask in.

With sign-in on, the person who started a run may reject it but not approve it: someone else must.
Without sign-in everyone is the same configured `user`, so they may. Either default can be changed:

```yaml
approvals:
  allow_self: false   # true lets people approve runs they started
```

A run started by a push records the commit's author name, which anyone can set in Git; it is not
checked against anyone's sign-in.

## Re-running from a step, and what each step was given

Every attempt of every step records what it was given when a runner took it: its environment, with
where each value came from (the step's own `env`, its container's, a template parameter, the
standard `LAUNCHWAY_*` values, or an output of a step it needs, named as `build · image`), its image
and container, its script, the commit of the library it came from, and which secrets it asked for.
Secrets are recorded by reference (`vault:ci/registry#token`), never by value: the server never sees
values. Very long values are shortened in the record (4 KiB a value, 16 KiB of script, 64 KiB an
attempt), and marked so; the step itself receives them in full. The run page shows them under
*Inputs* beside a step's outputs, attempt by attempt, and the API returns them with each step
(`inputs`).

A finished run can be **re-run from any step**: `POST /api/v1/runs/{id}/rerun` with
`{"from": "deploy"}`, or *Re-run from this step* in the UI, which first lists what will run and what
will be reused. The re-run is a new run, linked to the original (`rerun_of`, `rerun_from`), at the
same commit, with the same pipeline, the same library commits (even if a tag has moved since) and
the same trigger, started by whoever asked for it. The chosen step and every step after it run
again. Every other step is **reused**: it is recorded in the new run with the outcome and outputs
it had, marked as reused from the original, and is never run, so the steps after it receive exactly
the values they would have. Its log stays in the run where it ran, which the UI links to.

- Only steps that succeeded, or were skipped by their `when` condition, can be reused. If a step the
  chosen one needs did not succeed, re-run from an earlier step, or resume.
- Approvals after the step wait for a decision again. Approvals before it are reused with the
  decision made in the original, shown with who made it.
- A draft run's re-run is a draft run of the same text, with the same rules.
- Re-running needs the operator role on the project (and, for a draft, the right to start drafts),
  and is recorded in the audit trail as `run.rerun`, with the original run, the step and the new
  run.
- **Files are not reused.** A re-run starts from a fresh checkout, so files that reused steps wrote
  to the workspace are not there. Outputs are kept; steps that need files an earlier step made
  should be included in the re-run (re-run from that earlier step).

**From several steps at once.** Two independent branches of a run may both have failed. A re-run
takes several starting steps, `{"from": ["lint", "unit"]}`, and the steps that run again are those
after any of them; or `{"failed": true}`, the smallest set of steps that covers every step that
failed or was canceled (a failure the pipeline allows with `on_failure: continue` is not one). The
UI offers *Re-run failed steps* on a failed run when that can be done, with the same dialog that
lists what runs and what is reused; `GET …/rerun?from=a&from=b` or `?failed=true` answers the plan
without making a run. From the command line:

```sh
launchway runs rerun RUN --failed --plan      # what would run, and what would be reused
launchway runs rerun RUN --failed --wait
launchway runs rerun RUN --from lint --from unit
launchway runs resume RUN --wait
```

The rules for what is reused do not change: a step outside every starting step's followers that did
not succeed still refuses the re-run, saying which. The new run records all its starting steps
(`rerun_froms`; `rerun_from` is the first), and a server that does not know the list leaves such a
run alone.

**Resume or re-run?**

| | Resume | Re-run |
|---|---|---|
| Makes | no new run: the same run goes on | a new run, linked to the original |
| Runs again | the steps that failed, were canceled, or were not run because of them | the steps you name (or the failed ones) and everything after them |
| Reuses | every step that succeeded stays as it is | every other step, recorded as reused, with its outcome and outputs |
| Workspace files | the run's workspace on its runner is kept, for up to a day | a fresh checkout: files earlier steps wrote are gone |
| Deployments | refused once a deployment opened | deploy steps always run again, and are never reused |
| Use it | after a failure that has been put right | to go again from a chosen step, to redeploy what a run built, or when a fresh checkout is wanted |

The MCP tool `retry_run` does both: without steps it resumes, with `from_step`, `from_steps` or
`failed_only` it re-runs.
