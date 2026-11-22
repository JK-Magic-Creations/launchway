# Terraform and OpenTofu

Plan, review and apply infrastructure with OpenTofu (the open fork, the default) or Terraform, with the state kept by Launchway: no bucket, no backend block.

```yaml
pipeline: infrastructure
on:
  push: { branches: [main] }
  pull_request: {}
steps:
  infra:
    uses: launchway/infrastructure
    with: { environment: prod, workspace: prod }
```

On a pull request: format check, validation, a scan of the definitions, and the plan, whose summary is the pull request's check. On the main branch: the plan, then **Clearance** (a person approves, with the plan's page open), then the **apply of exactly that plan**.

## The plan is the reviewed thing

The plan step writes the plan file as the Cargo (artifact) `launchway-plan`, bound to the commit and to the state serial it was made against, and reads the JSON plan into a report: resources to add, change, destroy and replace, destroys and replacements **first**, attribute differences with sensitive values hidden, and a risk summary. Cost is not estimated.

The apply step fetches that file and **refuses it when it is stale**: made for another commit, or against a state whose serial has changed since (somebody else applied, or the state was returned to base). It says so and asks you to plan again. There is no option to apply a fresh, unreviewed plan in the Flight plan.

A plan made on a pull request or a draft run has no state credential (those Sorties (runs) may not read secrets), so it starts from empty state and every resource looks new. The report says so. It is a check that the code is valid, not a forecast.

## State

Launchway implements the HTTP backend protocol that Terraform and OpenTofu both speak, per Flight (codebase) and workspace: `GET`, `POST` and `DELETE` on the state, `LOCK` and `UNLOCK` with the client's lock document, `423` with the holder's document when it is taken.

- **Sealed at rest**: every version is sealed under the installation's root key, bound to its own row. A copy of the database holds none of it. A server with no root key refuses to keep state.
- **Credentials**: a step is given a credential for one workspace of its own Flight, valid while its Sortie goes, read-only for pull request and draft runs. People use their API token as the password.
- **Versions**: every accepted state is a version (who, when, which run, serial, resource count), 50 kept. A write with an older serial, another lineage, or the same serial and other content is refused.
- **Locks**: a lock taken by a Sortie that has ended is released by the next `LOCK`; nobody has to force it. Force-unlock needs a reason and is refused while the holder's run is going.
- **Return to base (rollback)**: stores an older version as the newest, with a higher serial. It changes the stored state only; no real resource changes, and the next plan shows the difference.
- **Never shown raw**: the Configuration page, the API, the CLI and the MCP server show resources and output names only. `launchway state pull` returns the document, needs a reason, and is recorded in the Logbook with your name. The MCP server never returns it.

```
launchway state list PROJECT
launchway state show PROJECT WORKSPACE
launchway state versions PROJECT WORKSPACE
launchway state rollback PROJECT WORKSPACE --version 3 --reason "the second apply was wrong"
launchway state lock PROJECT WORKSPACE --reason "maintenance window"
launchway state unlock PROJECT WORKSPACE --reason "window over"
launchway state pull PROJECT WORKSPACE --reason "debugging an import"
launchway state push PROJECT WORKSPACE state.json [--adopt]
```

To use the state from your own machine, set the backend (`backend "http" {}`) and `TF_HTTP_ADDRESS=https://launchway.example/api/state/v1/PROJECT/WORKSPACE`, `TF_HTTP_LOCK_ADDRESS` and `TF_HTTP_UNLOCK_ADDRESS` to the same address with `/lock`, `TF_HTTP_USERNAME` to anything, and `TF_HTTP_PASSWORD` to an API token. To move existing state, `tofu init -migrate-state`, or `launchway state push`.

If the repository declares a backend of its own, the step runs it as written and says plainly that Launchway cannot lock, version, show or return to base that state.

## Drift

A Patrol running the same plan with `fail_on_changes: true` (the Maneuver `launchway/tofu-plan`) exits with a failure when the infrastructure differs from the code; the Configuration page shows the latest plan per Airfield. Nothing is applied automatically.

## Credentials for providers

Give provider credentials by reference (`secrets:` on the step, or `TF_VAR_...` variables from a secret store). Short-lived credentials through OIDC federation with your cloud are designed and **not built yet**; until then use credentials that are short-lived on the cloud's side.

## Licenses

OpenTofu is open source (MPL-2.0) and the default image. Terraform is source-available, not open source; Launchway never bundles it. Set `tool: terraform` and name an image that has it, and read its license terms first. This is for your counsel.

## The Maneuvers

| Name | What it is |
|---|---|
| `launchway/infrastructure` | Flight plan: check, scan, plan, Clearance, apply |
| `launchway/tofu-apply` | Plan, Clearance and the apply of that plan |
| `launchway/tofu-plan` | Plan only (pull requests, drift) |
| `launchway/tofu-check` | `fmt -check` and `validate`, no state or credentials |
| `launchway/iac-scan` | Misconfiguration scan of the definitions (Trivy, Apache-2.0), with the checks built into the pinned scanner; `check_updates: true` downloads the newest, which may need a newer scanner. A scanner that reports an error of its own fails the step, in `report` mode too |
| `launchway/provision-and-configure` | Infrastructure, wait for the hosts, configure with Ansible, verify no drift |
