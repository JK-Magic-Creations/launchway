# User guide

How each feature of Launchway works, one page per feature. Each page describes what is on the
main branch today. For step-by-step lessons see the [tutorials](../tutorials/index.md); for every
key and setting see the [reference](../reference/index.md).

| Page | What it covers |
|---|---|
| [Pipelines](pipelines.md) | Steps, containers and services, outputs, superseding older runs, run inputs, loops, cleanup steps |
| [Triggers](triggers.md) | Pull requests, schedules, webhooks |
| [Editing pipelines](editor.md) | The web editor, draft runs of unsaved text, saving to Git |
| [Shared libraries](libraries.md) | Templates and scripts in Git, versions, parameters, overrides |
| [Approvals and re-runs](approvals-and-reruns.md) | Approval steps, re-running from a step, what each step was given |
| [Test reports](test-reports.md) | 13 formats, attachments, coverage, hosted HTML reports, flaky tests, history |
| [Artifacts](artifacts.md) | Files steps keep and fetch, storage, limits |
| [Variables at levels](variables.md) | A variable set at a level, overridden by a nearer one, locked from above, and how a pipeline takes it |
| [Build caches](build-caches.md) | Keeping dependencies between runs, keys, who can read and write what, limits |
| [Where artifacts are kept](../artifacts.md) | The database, filesystem and S3 stores, measurements, moving between them |
| [Security findings and SBOMs](security-findings.md) | SARIF findings, new and fixed, SBOMs, component search |
| [Container images](images.md) | BuildKit builds, scans, push rules, signing, provenance |
| [Deployments](deployments.md) | GitOps with Argo CD, rollouts, promote, abort, retry, rollback |
| [Policies](policies.md) | Rules, modes, waivers, policies from Git |
| [Verifying a deployment](verification.md) | Metrics and logs after a deploy, health sources, how the verdict is decided |
| [AI models](ai.md) | Optional models for steps, verdict explanations and failed steps |
| [Secrets](secrets.md) | Secret references, stores, masking |
| [The words Launchway uses](words.md) | Base, Wing, Squadron, Wingman, Logbook and the rest, in plain terms, and how to change them |
| [Sign-in and access](access.md) | OpenID Connect, roles, sessions, API tokens, audit trail |
| [Runners on other machines](runner-identity.md) | Pools, join tokens, runner identities, the runner's own limits |
| [Agents (MCP)](agents.md) | The MCP endpoint, tools, rights, limits |
| [Running several servers](high-availability.md) | One database, several servers, Kubernetes |
| [Using the command line](commands.md) | The `launchway` command |
