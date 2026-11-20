# Triggers

What starts a run besides a person: pushes and pull requests found by polling, schedules, and webhooks.

## Pull requests

`on: { pull_request: {} }` runs the pipeline for pull requests. There are no webhooks yet:
Launchway polls, as it does for pushes. Git hosts such as GitHub, Gitea and Forgejo publish each
pull request's head commit as the ref `refs/pull/<number>/head`; the server's mirror fetches it
with everything else, and a run starts when one appears or moves.

- **What runs.** The pipeline at the pull request's head commit, at that commit. The run is
  marked *pull request #12* wherever runs are shown, its event is `pull_request`, and its steps
  get `LAUNCHWAY_EVENT=pull_request`, `LAUNCHWAY_REF=refs/pull/12/head` and
  `LAUNCHWAY_PULL_REQUEST=12`. `LAUNCHWAY_BRANCH` is empty.
- **Who decides.** A pull request runs only if the pipeline at the tip of the project's default
  branch asks for pull requests, and so does the pipeline at its head. The default branch decides
  because it has been reviewed: a pull request cannot turn pull-request runs on for itself. When
  a pull request asks but the default branch does not, *Recent pushes* says so.
- **Pull requests that already exist do not run.** Git hosts keep these refs after a pull
  request is closed or merged. The first time a server looks for a project's pull requests (a new
  project, or the first poll after upgrading), it records every pull request's head and runs
  none of them. After that only new pull requests, and ones pushed to, run. Heads are recorded
  even while the default branch does not ask for pull requests, so turning them on later does
  not run the old ones either. As with pushes, this is kept in the database, and exactly one
  server acts on each change.
- **The target branch comes from the forge.** The ref says which commit a pull request is at, not
  which branch it targets, so `when: { branch: ... }` never matches a pull-request run. Use
  `when: { event: pull_request }` for steps only pull requests run, and `when: { event: [push,
  manual] }` to keep a step out of them. To run only for pull requests into some branches, write
  `on: { pull_request: { into: [main, "release/*"] } }`: the server then asks the project's forge
  connection which branch each new head targets (once for each head), and runs nothing for a
  pull request it cannot ask about. Steps get the answer as `LAUNCHWAY_PULL_REQUEST_INTO`. See
  [Pull request](../reference/pipeline.md#pull-request) for what the decisions say.
- **No secrets, by default.** A pull request's code may come from anyone who can open one, so a
  step of a pull-request run that declares `secrets` fails before any runner sees it, unless the
  project allows it. The setting is read when the run is created; resuming the run and re-running
  it keep that decision.

  ```yaml
  pull_requests:
    secrets: false          # the default
  projects:
    - name: payments
      repo: https://git.example.com/team/payments.git
      pull_requests: { secrets: true }   # overrides, per project
  ```

- **Not there yet:** reporting a run's outcome back to the pull request as a commit status. A Git
  host's [webhook](#webhooks) makes the server look at once instead of at its next poll.

## Schedules

```yaml
on:
  schedule:
    - { cron: "0 3 * * *", timezone: America/Chicago, ref: main, inputs: { environment: prod } }
    - { cron: "*/15 9-17 * * MON-FRI" }
```

A schedule is a five-field cron expression (minute, hour, day of the month, month, day of the
week; `@hourly`, `@daily`, `@weekly`, `@monthly` and `@yearly` work too) in a time zone (an IANA
name; UTC if none). When the clocks go forward, a time that does not exist that day is skipped;
when they go back, a time that happens twice fires once. `ref` is the branch or tag whose pipeline
runs (the default branch if none) and `inputs` the run's values (the others take their defaults).
Schedules are read only from the pipeline on the project's default branch, so a branch cannot
schedule itself. Runs have event `schedule`.

Each firing is claimed in the database, so however many servers are running, it starts one run.
A schedule never fires for a time before it was first seen (adding or changing one does not run it
for the past). If no server was running when a schedule should have fired, the first server to
look afterwards starts one run, for the latest time that was missed, and none for the earlier
ones. A firing that cannot start a run (the branch is gone, the pipeline there has problems) is
recorded with why. The project page lists the schedules, when each fires next, and how its last
firing went (`GET /api/v1/projects/{name}/schedules`).

`schedules: { enabled: false }` in the server's configuration turns schedules off for every
project, and a project's own `schedules:` overrides it. While off, firings are recorded as
skipped, so turning them back on does not catch up.

## Webhooks

Two kinds of webhook reach the server. Both prove themselves with a secret that the server's
configuration names as an environment variable or a file (a Kubernetes Secret, for example); the
secret is never in the repository or the database. A caller that cannot prove itself gets the
same `404` whatever the reason (no such project, no webhook, a wrong secret), is recorded in the
audit trail, and counts towards the [limit on failed attempts](access.md#api-tokens).

**A Git host's webhook** makes the server poll a project at once, instead of at its next poll. It
starts nothing by itself: the poll decides, as it always does, so a forged or repeated call could
at most cause a poll. Point the host's push and pull request webhook at
`https://launchway.example.com/api/v1/projects/<name>/git-hook`:

```yaml
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    git_webhook: { kind: github, secret_env: PAYMENTS_HOOK_SECRET }   # or secret_file
```

`kind` is `github` (checks `X-Hub-Signature-256`), `gitea` or `forgejo` (`X-Gitea-Signature` or
`X-Forgejo-Signature`), or `gitlab` (`X-Gitlab-Token`). Each delivery's ID is remembered for a
week; a delivery seen before is answered and ignored.

**A webhook trigger** lets another system start a run, with inputs taken from its request's JSON
body by a closed mapping (no expressions):

```yaml
on:
  webhook:
    name: deploy-request
    ref: main                        # whose pipeline runs; the default branch if none
    inputs: { version: body.release.version, environment: body.environment }
```

`webhook` may also be a list of these. Like schedules, they are read from the default branch. The
values are checked exactly like a person's: a missing field takes the input's default, and a value
that does not fit is refused with `422` and a message per input. Runs have event `webhook`. The
server's configuration says what callers must prove; a trigger it does not list cannot be called:

```yaml
projects:
  - name: payments
    webhooks:
      deploy-request: { hmac_secret_env: PAYMENTS_DEPLOY_HMAC }   # or hmac_secret_file,
                                                                   # token_env, token_file
```

Call it with `POST /api/v1/projects/payments/webhooks/deploy-request` and a JSON body of at most
64 KiB, either with `Authorization: Bearer <token>`, or signed: `X-Launchway-Timestamp: <Unix
seconds>` and `X-Launchway-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>">`. A signed
request is accepted within five minutes of its timestamp, and only once. Prefer signing: a token
can be replayed by anyone who sees one request. Send `Idempotency-Key: <up to 200 characters>` to
make retries safe: a repeat answers `200` with the run the first call started (and
`Idempotent-Replayed: true`), and the same key with a different body is refused with `409`. Keys
are remembered for a week. Every call is in the audit trail (`webhook.run`).
