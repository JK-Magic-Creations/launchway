# User guide

How each feature of Launchway works, one page per feature. Each page describes what is on the
main branch today. For step-by-step lessons see the [tutorials](../tutorials/index.md); for every
key and setting see the [reference](../reference/index.md).

| Page | What it covers |
|---|---|
| [Pipelines](pipelines.md) | Steps, containers and services, outputs, superseding older runs, run inputs, loops, cleanup steps |
| [Triggers](triggers.md) | Pull requests, schedules, webhooks |
| [Editing pipelines](editor.md) | The web editor, draft runs of unsaved text, saving to Git |
| [Starters](starters.md) | Flight plans and step templates that ship with Launchway, pipelines with no file, the gallery |
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
| [GitOps write-back](gitops-write-back.md) | Several pipelines writing one repository, a tag with its digest, and writing to a repository Launchway hosts: direct or by pull request |
| [Verifying a deployment](verification.md) | Metrics and logs after a deploy, health sources, how the verdict is decided |
| [Notifications](notifications.md) | Telling people an approval waits or something went wrong: channels, rules, the delivery log |
| [AI models](ai.md) | Optional models for steps, verdict explanations and failed steps |
| [Git over SSH](git-over-ssh.md) | SSH keys, deploy keys, host key fingerprints, protected branches, certificates, limits |
| [Pull requests and code review](pull-requests.md) | Opening, reviewing and merging changes on hosted repositories; merge rules; checks |
| [Webhooks and events](webhooks.md) | Repository and owner webhooks, signed deliveries, the delivery log, redelivery, turning off, pipelines on push |
| [Secrets](secrets.md) | Secret references, stores, masking |
| [Upgrading](upgrading.md) | From the release before: the root key, sealing, sign-in, values to check, rolling back |
| [Encryption at rest](encryption-at-rest.md) | The root key, what is sealed, backing the key up, rotating and replacing it |
| [The words Launchway uses](words.md) | Base, Wing, Squadron, Wingman, Logbook and the rest, in plain terms, and how to change them |
| [Sign-in and access](access.md) | OpenID Connect, roles, sessions, API tokens, audit trail |
| [Runners on other machines](runner-identity.md) | Pools, join tokens, runner identities, the runner's own limits |
| [Agents (MCP)](agents.md) | The MCP endpoint, tools, rights, limits |
| [Accessibility](accessibility.md) | What is tested for keyboard, screen reader and contrast use, and the known gaps |
| [Running several servers](high-availability.md) | One database, several servers, Kubernetes |
| [Using the command line](commands.md) | The `launchway` command |
