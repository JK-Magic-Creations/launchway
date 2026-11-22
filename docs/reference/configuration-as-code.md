# Configuration as code reference

The files, formats and protocol behind [Ansible](../guide/ansible.md) and [Terraform and OpenTofu](../guide/terraform-opentofu.md). The routes are in the [HTTP API](http-api.md); the commands are `launchway state` and `launchway configuration` (see [command line](cli.md)).

## What a configuration step keeps

A step keeps its files as the Cargo (artifact) `launchway-config`:

| File | Written by | Read as |
|---|---|---|
| `.launchway/ansible/report.json` | the `launchway_report` callback of the Ansible Maneuver (format `launchway-ansible-report/1`) | an Ansible run report |
| `.launchway/tofu/plan.json` and `meta.json` | `tofu show -json` of the plan, and the script's sidecar (tool, state serial, commit, workspace, Airfield, exit code, state backend) | a plan report |

The server reads each once per step attempt, redacts it again, and stores the parsed report with the Sortie (migration 51, table `cac_reports`). A report that cannot be read is stored as `unreadable` with the reason. A plan file (`plan.bin`) is a separate Cargo, `launchway-plan`; it holds planned values, so it is as sensitive as state.

## The Ansible report

Per play, per task, per host: `status` (`ok`, `changed`, `failed`, `skipped`, `unreachable`), timing, the message of a failure, and in diff mode the unified diff of files (cut at 20,000 characters). A task marked `no_log` has no message and no diff. Hosts are classified by their worst result: `failed`, `unreachable`, `changed`, `ok`. The recap is `N hosts: C changed, O ok, F failed`, with `unreachable` added when there are any; a check-mode run says `would change`.

Limits: 32 MiB per report, 20,000 tasks, 5,000 hosts. The callback is written for ansible-core 2.19 and enabled by environment variables, so a repository's `ansible.cfg` cannot switch it off.

## The plan report

Resources to add, change, destroy and replace, ordered destroys, replacements, updates, creates. Attributes are listed as dotted paths with values shortened to 160 characters; attributes the plan marks sensitive are listed without values; unknown values say `(known after apply)`. The risk is `high` when anything is destroyed or replaced, `low` for changes, `none` otherwise.

## The state backend

Address: `/api/state/v1/{project}/{workspace}`; lock address: the same with `/lock`. Workspace names are lower case letters, digits, `-` and `_`.

| Part of the protocol | Supported |
|---|---|
| `GET` state, `404` when none | yes |
| `POST` (and `PUT`) state, `?ID=` lock id, `Content-MD5` checked when sent | yes |
| `DELETE` state | yes, refused while locked |
| `LOCK` / `UNLOCK` with the client's lock document; `423` with the holder's document | yes (the defaults of both tools) |
| Lock with other methods (`POST`, `PUT`, `DELETE`) | no: leave `lock_method` and `unlock_method` at their defaults |
| `terraform workspace` commands | no: a workspace is part of the address |
| The remote backend of Terraform's hosted service | no, by decision |

Authentication is HTTP basic. The password is a state credential (`lwst_...`, made for one workspace of one Flight (codebase) and one step of one Sortie, valid while the Sortie goes, at most three hours) or an API token; the user name is ignored. Credentials of Sorties are asked for by a step with a reference to the reserved secret store `launchway`: `launchway:state#WORKSPACE`; the Station (runner) also sets `TF_HTTP_ADDRESS`, `TF_HTTP_LOCK_ADDRESS`, `TF_HTTP_UNLOCK_ADDRESS`, `TF_HTTP_LOCK_METHOD`, `TF_HTTP_UNLOCK_METHOD` and `TF_HTTP_USERNAME`.

Rules: the first write fixes the lineage; a lower serial, another lineage, or the same serial with other content is `409`; a write under another client's lock is `423`; a state over 64 MiB is refused; 50 versions are kept per workspace.

## Parameters of the Maneuvers

Each Maneuver lists its parameters in its file under `internal/starters/library/templates/`: `ansible-run`, `ansible-playbook`, `ansible-lint`, `tofu-plan`, `tofu-apply`, `tofu-check`, `iac-scan`, `infrastructure`, `provision-and-configure`.
