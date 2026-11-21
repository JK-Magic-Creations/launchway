# Pull-request trust

A pull request runs code somebody wrote. When that somebody is a dependency bot, or a fork, nobody
on the team has read the change yet, and the run should not be able to read your secrets, launch (deploy),
or poison a cache that the main build reads. Launchway runs such pull requests in a **restricted
mode** until a maintainer has looked.

## Who is untrusted

- A pull request from an **automation account**: an author or a person who started the Sortie (run) whose
  name matches `automation.accounts` (by default `*[bot]`, `dependabot*`, `renovate*`,
  `github-actions*`, `release-please*`, `launchway-updater*`; matching ignores case). Accounts that a forge hosted by
  Launchway marks as bots count too.
- A pull request from a **fork**, where the forge says so. (A project that is polled from another
  Git host cannot tell a fork from a branch, so only the automation list applies there.)

The policy is `pull_requests.untrusted`: `restrict` (the default) or `allow`, in the installation
settings, with a project able to override it.

## What a restricted run may not do

| Restriction | What happens |
|---|---|
| Secrets | No step reads one, even if the project sets `pull_requests.secrets: true`. A step that asks for one fails at once with the reason, and is never handed to a Station (runner) |
| Launches | Launch, Return to base (rollback) and rollout steps are skipped, with the skip kind `restricted` and the reason |
| Caches | The Sortie restores the default branch's cache and saves none |
| Depot (registry) | Image steps build and scan, never push (as for every pull request), and use no pull credentials |
| Pipeline | The pipeline is the one on the **default branch**, not the one in the pull request, so a pull request cannot rewrite its own checks |

The Sortie page says so on top: *restricted: pull request from an automation account (dependabot[bot]):
it runs without secrets, launches, caches or Depot pushes until a maintainer approves it*, and the
API gives it as the Sortie's `restriction` with a stable `code` (`automation_account` or `fork`).

## Run with secrets

A maintainer who has read the change chooses **Run with secrets** on the Sortie (the Sortie's action
`run_with_secrets`, `launchway runs trust <run>`, the MCP tool `trust_run`, or
`POST /api/v1/runs/{id}/trust`). It is the same kind of decision as a Go/No-go: recorded in the
run's events (`run.trusted`) and the Logbook with the person's name and comment. From then on
the steps of that run that have **not started** may read secrets and launch. A step that was
already skipped or failed because of the restriction is not started again by approving; re-run it
from that step (a re-run of a restricted run is restricted again, until it is approved).

An administrator who wants no restriction sets `pull_requests.untrusted: allow`.
