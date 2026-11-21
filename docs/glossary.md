# Glossary

The terms used in Launchway and these documents, in alphabetical order. [Concepts](start/concepts.md)
explains the main ones and how they relate.

### Access binding
An entry in `auth.access` that grants a [role](#role) to a group or user on matching projects,
optionally limited to environments.

### Admin
The highest [role](#role): an operator who may also read the audit trail, save a pipeline that
has problems, and waive policy rules (by default).

### API token
A secret starting `lw_` that scripts and the command line send as `Authorization: Bearer`. It acts
as the person who made it, always expires, and may be limited to a role and projects.

### Approval
A step of `kind: approval` that waits for a person to approve or reject it. The screens call it a [clearance](#clearance), answered with Go or No-go, in the default words.

### Artifact
A set of files a step keeps, for people to download and for later steps to fetch.

### Audit trail
The plain business word for the [Logbook](#logbook). The append-only record of every change, sign-in and refused call: who, when, from where, what and
the outcome.

### Base
The default word for the top level: the whole installation. The plain business word is *Installation*. See [the words Launchway uses](guide/words.md).

### Blob store
Where artifacts are kept: the database (the default on PostgreSQL), a directory, or S3-compatible
object storage. See [Where artifacts are kept](artifacts.md).

### BuildKit
The container image builder Launchway's image steps use, run rootless apart from the steps.

### Checklist
The list of things that must be true before a change goes on. The same word in both sets.

### Clearance
The default word for an approval: a person decides whether a change may go on, by saying [Go or No-go](#go-and-no-go). The plain business word is *Approval*. In pipelines it is still a step of `kind: approval`.

### Deploy runner
A [runner](#runner) with a `deploy:` section that carries out deploy, rollback and rollout steps
for the targets of its [runner pool](#runner-pool). It never runs a script.

### Deploy target
A cluster a deploy runner serves, named in its configuration and in the server's environments.

### Detachment
The default word for the agent on a customer's side that does the work and holds the data, so the hosted service holds nothing sensitive. Planned: see [the hosted service](roadmap/hosted-service.md). Not called a connector or a relay.

### Deployment
The record of one deploy step's change to an [environment](#environment): version, commit,
observations and outcome.

### Draft run
A run of pipeline text that has not been saved to Git, started from the editor. It cannot deploy,
and by default its steps cannot read secrets.

### Environment
A place a project deploys to, such as `prod`, configured on the server, with one deployment at a
time per service.

### Event
One recorded change to a run, such as `step.finished`. A run's state is rebuilt from its events.

### Executor
How a runner runs a step: in a Podman or Docker container, in a Kubernetes pod, or on the host.

### Finding
One result of a security scanner, read from SARIF, with a severity and a fingerprint.

### Flight plan
The default word for the ready-made pipeline offered for a kind of project. The plain business word is *Starter pipeline*.

### GitOps
Deploying by committing the desired version to a Git repository that a tool (here Argo CD) applies
to the cluster.

### Go and No-go
The two answers to a [clearance](#clearance): Go lets the run continue, No-go fails the step and skips what follows. The plain business words are *Approve* and *Reject*; the API paths stay `approve` and `reject`.

### Health source
A monitoring system (Prometheus, Loki, ClickHouse, Elasticsearch, PostgreSQL or a JSON API) that a
verify step reads metrics and logs from. An operator configures it on a deploy runner; pipelines only
name it.

### Hold
The default word for a freeze: a time when changes may not go out. The plain business word is *Freeze*.

### In flight
A change on its way to production, shown on the [launch line](#launch-line). The plain business words are *in progress*.

### Input
A typed value a run is started with, declared under `inputs:` in the pipeline.

### Join token
A secret, shown once, that a person makes for a runner pool. A runner uses it to enrol: it registers
its own key and gets an identity. See [Runners on other machines](guide/runner-identity.md).

### Launch line
The line a change travels along, from commit to production, drawn on Home and on every run. Kept in both sets because it comes from the product's name.

### Lease
A runner's time-limited claim on a step. A runner that stops renewing it loses the step, which is
run again.

### Library
A Git repository of [templates](#template) and the scripts they run, named in the server's
configuration.

### Logbook
The default word for the [audit trail](#audit-trail): the append-only record of who did what, and when. Found at **Settings → Logbook**.

### MCP
The Model Context Protocol, which AI agents use to call tools. Every Launchway server serves it at
`/mcp`, with the rights of the token the agent holds. See [Agents (MCP)](guide/agents.md).

### Operator
The [role](#role) that may start, cancel, resume and re-run runs, approve, and save pipelines.

### Output
A `key=value` a step writes to `$LAUNCHWAY_OUTPUTS`, given to steps that need it.

### Payload
What a change delivers: the commit, images and files it carries. The plain business word is *Contents*.

### Pipeline
The file `.launchway/pipeline.yaml` that says when to run and which steps to run.

### Policy
A set of typed rules the organization sets for every pipeline, enforced or reported, judged at run
start, before deploying and before pushing an image.

### Preflight
The checks made before a deploy. The plain business word is *Checks*.

### Project
A Git repository Launchway watches and runs pipelines for.

### Provenance
A signed statement of how an image was built: from what source, with what, by which runner.

### Registry
Launchway's own store for container images, Helm charts and other OCI artifacts, served under `/v2/` on the server's address. A repository in it is named `PROJECT/NAME`, and the project's roles decide who may pull, push and delete. See [The built-in registry](guide/registry.md).

### Re-run
A new run that starts again from a chosen step, reusing the results of the steps before it.

### Resume
Continuing a failed or cancelled run: only the steps that did not succeed run again.

### Role
Viewer, operator or admin, held per project.

### Rollback
A step of `kind: rollback` that reverts a deployment's commit when a step guarding it does not
succeed.

### Rollout
An Argo Rollouts canary or blue-green rollout. A `kind: rollout` step promotes, aborts or retries
one.

### Run
One execution of a pipeline at one commit.

### Runner
The program, `launchway runner`, that takes steps from a server and runs them. Runners connect
outbound only.

### Runner pool
A named group of runners. A deploy pool serves some deploy targets; a build pool serves some
projects, and optionally only some events. Runners enrol into a pool with a join token (or, for a
deploy pool, use its token).

### SARIF
A standard file format for the results of static analysis and security scanners.

### SBOM
Software bill of materials: the list of components in a piece of software. Launchway reads
CycloneDX JSON.

### Secret store
Where a runner reads secret values: environment variables, files, or Vault or OpenBao.

### Squadron
The default word for the optional group of projects with shared settings: the third level. The plain business word is *Team*.

### Step
One unit of work in a run.

### Supersede
Cancelling older queued or running runs of the same branch or pull request when a newer one starts.

### Template
A reusable fragment of pipeline with typed parameters, kept in a [library](#library).

### Verification
A step of `kind: verify` that watches a deployment's metrics and logs for a window of time and
decides passed, failed or inconclusive. See [Verifying a deployment](guide/verification.md).

### Viewer
The [role](#role) that may see a project's runs, logs and pipelines, but not change anything.

### Variable
A named, typed setting set at a [level](#base) (Base, Wing, Squadron or Project), for everything or for one environment. A nearer level overrides a wider one and a lock stops that. A secret variable is a reference, never the secret. See [Variables at levels](guide/variables.md).

### Variable lock
A lock on a variable at a level, with a reason: nobody below the level may change it, a pipeline file and a run included. In code terms, `final`. See [Variables at levels](guide/variables.md#locks).

### Waiver
A recorded, expiring exception that lets one policy rule pass for a project, with a reason.

### Wing
The default word for the organization that owns the projects: the second level. The plain business word is *Organization*.

### Wingman
The default word for the helper and guide panel beside every page. The plain business word is *Guide*.
