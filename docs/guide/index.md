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
| [Test reports](test-reports.md) | JUnit XML and `go test -json`, flaky tests, history |
| [Artifacts](artifacts.md) | Files steps keep and fetch, storage, limits |
| [Security findings and SBOMs](security-findings.md) | SARIF findings, new and fixed, SBOMs, component search |
| [Container images](images.md) | BuildKit builds, scans, push rules, signing, provenance |
| [Deployments](deployments.md) | GitOps with Argo CD, rollouts, promote, abort, retry, rollback |
| [Policies](policies.md) | Rules, modes, waivers, policies from Git |
| [Secrets](secrets.md) | Secret references, stores, masking |
| [Sign-in and access](access.md) | OpenID Connect, roles, sessions, API tokens, audit trail |
| [Running several servers](high-availability.md) | One database, several servers, Kubernetes |
| [Using the command line](commands.md) | The `launchway` command |
