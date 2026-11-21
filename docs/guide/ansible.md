# Ansible

Configure machines with Ansible from a Flight plan. Launchway runs `ansible-playbook` inside a pinned execution image, in a step, and reads the result into a Sortie (run) report: hosts by plays, what changed, the failed task first, and the hosts to run again. Nothing but the database and Git is needed on the Launchway side; the tool itself is yours.

```yaml
pipeline: web-servers
on:
  push: { branches: [main] }
inputs:
  limit: { type: string, default: "" }
steps:
  configure:
    uses: launchway/ansible-playbook
    with:
      environment: prod
      playbook: site.yml
      hosts: "web1=10.0.0.11,web2=10.0.0.12"
      known_hosts: "web1 ssh-ed25519 AAAA...\nweb2 ssh-ed25519 AAAA..."
      auth: key
      ssh_key_store: vault
      ssh_key_key: ansible/deploy
      limit: { input: limit }
```

The Flight plan lints the playbook (advice), runs it in **check mode** (changes nothing, reports what would change), asks for **Clearance** before the real run, then runs it. A pull request runs the check only.

## The execution image

Launchway does not bundle Ansible. Build the default image from `packaging/ansible-ee/Containerfile` (ansible-core, ansible-lint and the SSH client, all version-pinned), push it to your Depot (registry), and name it in the `image` parameter; pin it by digest in production. Bring your own image if your playbooks need more, as long as it has `ansible-playbook`, `ansible-galaxy` and `python3`.

## What the Maneuver takes

`launchway/ansible-run` is the Maneuver under the Flight plan; use it directly for one Sortie. Its parameters:

| Parameter | Meaning |
|---|---|
| `playbook`, `inventory`, `hosts`, `limit` | The playbook; an inventory file, directory or plugin in the repository, or `inline` with `hosts` (`name=address[:port]`, comma separated); only these hosts |
| `tags`, `skip_tags`, `forks`, `user`, `become`, `become_user` | The usual switches |
| `check`, `diff` | Check mode; show the change of files (on by default) |
| `extra_vars` | A JSON object: typed inputs and variables by level. Written to a file in memory, never put on the command line |
| `secret_vars` | `name=ENVIRONMENT_VARIABLE` pairs for values the step was given by secret reference |
| `auth`, `ssh_key_*`, `vault_*` | `none`, `key` (an SSH private key by reference) or `key+vault` (and the Ansible vault password by reference) |
| `known_hosts` | The hosts' public keys. **Pinned**: with none, the Sortie refuses to connect; a changed key fails the Sortie |
| `requirements` | Collections and roles to install, kept in a cache keyed by the hash of the file and the image |
| `environment` | The Airfield this configures, for the Configuration page and drift |

## Secrets

The SSH key and the vault password are **references** (`vault:ansible/deploy#private_key`): the Station (runner) looks them up, writes them to memory-backed storage for the Sortie, removes them, and masks them in every log. The Sortie report is read with `no_log` respected, and credential-like text (keys, `password=`, tokens, vault blobs) is removed again by the server before anything is stored. The host key is pinned; a certificate authority that gives each run a five-minute SSH certificate is designed and not built yet.

## The Sortie report {#the-run-report}

Open a Sortie's Configuration report from the project's **Configuration** page, or with `launchway configuration report RUN STEP`. It shows:

- the recap line, also on the Sortie: `12 hosts: 9 changed, 3 ok, 0 failed` (in check mode, `would change`);
- the failed task with its message at the top, or the host that could not be reached;
- hosts by plays, filterable by host and outcome;
- what changed, with the diff when diff mode is on;
- the button **Run again on the failed hosts**, which starts a run with `limit` set to exactly those hosts (the pipeline takes the limit from an input named `limit`, as above; a run on the same branch starts from its current head).

## Drift

Run the same Flight plan on a schedule with `check: true`. The result is the drift per host: `3 hosts drifted: files X, service Y`. The Configuration page shows the latest check per Airfield, and a person opens the apply run from there; the apply waits for Clearance first, and the Clearance request is on the check run's page, where the approver sees exactly what will change, host by host. Applying is never automatic.

"Return to base" for configuration is honest about what it is: **apply the previous definition**. Re-run the playbook of the previous good commit. It is not time travel; a task that is not idempotent, or a change Ansible does not undo, stays.

## Batches

Use Ansible's own `serial:` in the playbook, with a health `check:` as the last task of each batch (a `uri` or `command` task that fails the host). A failed host stops the next batch, as Ansible does by default (`any_errors_fatal` makes it stop everything). Launchway does not reimplement this; the report shows which batch failed.

## Testing a role

`launchway/ansible-lint` lints. For role tests, run Molecule in the execution image in an ordinary step; Launchway has no special support for it yet.

See also [Terraform and OpenTofu](terraform-opentofu.md) and [Variables at levels](variables.md).
