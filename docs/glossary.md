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
A step of `kind: approval` that waits for a person to approve or reject it.

### Artifact
A set of files a step keeps, for people to download and for later steps to fetch.

### Audit trail
The append-only record of every change, sign-in and refused call: who, when, from where, what and
the outcome.

### Blob store
Where artifacts are kept: the database (the default on PostgreSQL), a directory, or S3-compatible
object storage. See [Where artifacts are kept](artifacts.md).

### BuildKit
The container image builder Launchway's image steps use, run rootless apart from the steps.

### Deploy runner
A [runner](#runner) with a `deploy:` section that carries out deploy, rollback and rollout steps
for the targets of its [runner pool](#runner-pool). It never runs a script.

### Deploy target
A cluster a deploy runner serves, named in its configuration and in the server's environments.

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

### GitOps
Deploying by committing the desired version to a Git repository that a tool (here Argo CD) applies
to the cluster.

### Health source
A monitoring system (Prometheus, Loki, ClickHouse, Elasticsearch, PostgreSQL or a JSON API) that a
verify step reads metrics and logs from. An operator configures it on a deploy runner; pipelines only
name it.

### Input
A typed value a run is started with, declared under `inputs:` in the pipeline.

### Join token
A secret, shown once, that a person makes for a runner pool. A runner uses it to enrol: it registers
its own key and gets an identity. See [Runners on other machines](guide/runner-identity.md).

### Lease
A runner's time-limited claim on a step. A runner that stops renewing it loses the step, which is
run again.

### Library
A Git repository of [templates](#template) and the scripts they run, named in the server's
configuration.

### MCP
The Model Context Protocol, which AI agents use to call tools. Every Launchway server serves it at
`/mcp`, with the rights of the token the agent holds. See [Agents (MCP)](guide/agents.md).

### Operator
The [role](#role) that may start, cancel, resume and re-run runs, approve, and save pipelines.

### Output
A `key=value` a step writes to `$LAUNCHWAY_OUTPUTS`, given to steps that need it.

### Pipeline
The file `.launchway/pipeline.yaml` that says when to run and which steps to run.

### Policy
A set of typed rules the organisation sets for every pipeline, enforced or reported, judged at run
start, before deploying and before pushing an image.

### Project
A Git repository Launchway watches and runs pipelines for.

### Provenance
A signed statement of how an image was built: from what source, with what, by which runner.

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

### Waiver
A recorded, expiring exception that lets one policy rule pass for a project, with a reason.
