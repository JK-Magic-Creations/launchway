# Configuration objects

Everything an administrator manages is an object of one of the kinds below, kept in the database
with a version and a history. This page lists the kinds, their settings, and how the API, the
command line, the MCP server and the planned Terraform provider name them. For the ideas, see
[Configuration and levels](../guide/configuration.md).

## How objects work

- **A spec** is the object's settings, written with the keys the configuration file uses: a
  project's `repo`, `branch`, `poll`, `pipeline`. Settings left at their default are not stored.
- **Names are stable.** An object is found by its kind and name; the name never changes. Names are
  letters, digits, `.`, `_` and `-`, starting with a letter or digit, at most 100 characters.
- **Versions.** Each change raises the object's `version`, which is also the `ETag`. A change that
  names the version it expects (`If-Match`, or `version` in the body) is refused with `412` if
  someone else changed the object meanwhile. A change with no version replaces whatever is there.
- **Checks.** A change is checked as part of the whole configuration, by the rules the configuration
  file has always been held to (one validator): two changes that are each valid but not together,
  such as deleting an environment a pool still serves, cannot both be made. Every problem is listed.
- **History.** Every change is recorded with who made it, when, the action, the settings before and
  after, and an optional comment. History is never rewritten.
- **Deleting is reversible.** A deleted object is kept and `restore` brings it back as it was. The
  installation settings, the organisation and the step resources cannot be deleted; change them.
- **Managed by.** `managed_by` says who owns an object: `api` (the UI, the CLI, MCP or a direct
  call), `import`, `terraform` or `git` (a tool says which with the header `X-Launchway-Manager`, and
  only with an API token: a signed-in browser is always the web UI). The label is enforced, not
  advisory: an object that Terraform or Git manages is read-only to everyone else, because the next
  run of the tool would undo their change. Whoever really needs to change it sends `take_ownership`
  (`--take-ownership`, or the confirmation in the web UI): it makes them the owner and is recorded in
  the object's history and the audit trail. Sending the header alone never changes who owns an
  object; claiming one the UI manages needs `take_ownership` too.
- **Units.** Objects that can be defined at a level carry a `unit`; empty is the organisation. A
  project's `unit` is the unit it belongs to.

## The kinds

| Kind | Path and CLI name | What it holds | Terraform (planned) |
|---|---|---|---|
| Project | `projects` | A repository: `repo`, `branch`, `poll`, how runs start (`pipeline.on`), a pipeline from a library template (`pipeline.library`, `version`, `template`, `with`, `supersede`), pull-request settings, drafts, schedules, Git webhook, push settings | `launchway_project` |
| Library | `libraries` | A repository of templates and scripts pipelines may use by name: `repo` | `launchway_library` |
| Environment | `environments` | Where deploys may go: `target`, `projects`, `queue` (`newest` or `all`), `protected` | `launchway_environment` |
| Runner pool | `runner-pools` | A group of runners: `kind` (`deploy` or `build`), `targets`, `projects`, `events`, `require_identity`, `token_env` | `launchway_runner_pool` |
| Role binding | `role-bindings` | A user or group, a role (`viewer`, `operator`, `admin`), the `projects` and `environments` it covers | `launchway_role_binding` |
| Policy | `policies` | A delivery policy: `mode` (`enforce` or `report`), `scope`, and rules from the [policy rule catalogue](policy-rules.md) | `launchway_policy` |
| Policy source | `policy-sources` | A library whose files hold policies, at a pinned version | `launchway_policy_source` |
| Health source | `health-sources` | A metrics or logs system verify steps may read by name: `type`, `projects`. Its address and credentials stay with the deploy runners | `launchway_health_source` |
| AI model | `models` | A model steps may ask: provider, endpoint, model, `key` (a reference to a secret store, never a value), `projects`, limits | `launchway_model` |
| Secret store | `secret-stores` | The definition of a secret store pipelines may name: `type`, `projects`. Its address and credentials stay with the runners | `launchway_secret_store` |
| Step resources | `step-resources` | The default CPU and memory by kind of step (`defaults`) and the most any step may ask for (`limits`). One object, named `step-resources` | `launchway_step_resources` |
| Unit | `units` | A group of projects: `display_name`, `description` | `launchway_unit` |
| Installation settings | `installation` | What the levels are called (`levels`), approvals, drafts, pull requests, schedules, loops, tests, findings, reports, artifact limits and where artifacts are kept (`artifacts`), the MCP endpoint (`mcp`), the role needed to manage runners (`runner_admin`), policy waivers, API token limits. One object, named `installation` | `launchway_installation` |
| Organisation | `organisation` | `display_name` and `description`. One object, named `default` | `launchway_organisation` |

Secrets are never values: an AI model's `key` and a pipeline's secrets are references (`store`, `key`,
`field`) to a store the runner can read, and sign-in's client secret is read from an environment
variable.

### An example

```yaml
# launchway config set projects shop -f shop.yaml
repo: https://git.example.com/team/shop.git
branch: main
poll: 1m
pipeline:
  library: ci-templates
  version: v3
  template: go-service
  with: { image: shop }
  "on":
    push: { branches: [main], tags: ["v*"] }
    pull_request: {}
  supersede: true
```

## Where artifacts are kept, and the MCP endpoint

`installation` holds `artifacts.store` (`database`, `filesystem` or `s3`), `artifacts.dir`,
`artifacts.s3` and `artifacts.database`, and the whole `mcp:` section (`enabled`, `allowed_origins`,
`requests_per_minute`, `concurrent`, `total`, `oauth`). The MCP settings apply at once, including
turning the endpoint off and on. The artifact store is chosen when a server starts, so a change to
it takes effect when each server next restarts; copy the artifacts first with `launchway blobs
copy`. Object-store credentials are never stored: servers read them from the environment.

## Step resources

The `step-resources` object is the one place to change the [step resources](pipeline.md#resources):

```yaml
limits: { max_cpu: "8", max_memory: 16Gi, memory_per_thread: 1Gi }
defaults:
  step: { cpu: "1", cpu_limit: "4", memory: 2Gi, memory_limit: 6Gi }
```

The older routes (`PUT /api/v1/step-resources/...`, `launchway step-resources`) change the same
object, so there is one history. Before the first change the object reads as what the installation
holds, at version 0.

## Settings that name a file on the server

`token_file`, `secret_file`, `hmac_secret_file`, `ca_file` and `artifacts.dir` name files and directories on the
server host, and a repository address that is a path on the server (or a `file:` address) reads one. They are the
operator's to set, not an API administrator's: the admin role on every project is not a login on the server, but
a server that reads whatever file such a setting names would turn it into one (its own service-account token, for
one). So they are set in the bootstrap file, and local repository paths also need `egress.allow_local` there. The
first start's import brings them into the database; the API, the web UI, the CLI, MCP and Terraform keep what is
there, may change everything else about the object, and refuse a change to the path itself, naming it. Use a
secret store reference, an environment variable (`token_env`) or an `https://` address where you can.

## Terraform

The provider is the next item; the API is shaped for it. Each kind is one resource (names above) and
one data source; the resource's ID is `{kind}/{name}`, so `terraform import launchway_project.shop
shop` reads the object by name; an update is `PUT` with the version Terraform read, so a plan that
raced a change fails instead of overwriting it; `PUT` of the same settings changes nothing, so a plan
with no differences applies nothing; nothing is write-only (secrets are references). Terraform sets
`X-Launchway-Manager: terraform`, which the UI shows.

## Import, export and the file

`launchway config export` writes objects as configuration text: the sections a configuration file
has (`projects:`, `environments:`, `runner_pools:`, `policy:` and so on) and, for what the file has
no place for (units, secret stores, step resources, named role bindings, anything defined at a unit),
an `objects:` list. `launchway config import` reads the same text, plans, and applies on request. An
older server that still runs from a file can use an export with the `objects:` list removed.

Settings that stay in the file are not exported: see [Server configuration](server-config.md).
