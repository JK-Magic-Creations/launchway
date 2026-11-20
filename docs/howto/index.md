# How-to guides

Task-focused guides for people who install and run Launchway. Each guide says what was verified
and what was not.

## Install

| Guide | For |
|---|---|
| [Install on a laptop](install-laptop.md) | a developer's own machine |
| [Install on one server](install-single-server.md) | a small team, one Linux machine |
| [Install on Kubernetes (HA)](install-kubernetes.md) | a shared service with several servers |
| [Install on Amazon EKS](install-eks.md) | Kubernetes on AWS with managed PostgreSQL |
| [Install on Google GKE](install-gke.md) | Kubernetes on Google Cloud with managed PostgreSQL |

## Set up, with a wizard

Each is a guided flow in the web UI under Configuration: it asks plain questions, explains each option,
checks your answers, and shows what it will create before it creates anything.

| Guide | For |
|---|---|
| [First-run setup](setup-first-run.md) | naming things, signing in, the first project |
| [Connect a repository](connect-repository.md) | a repository, its branch, how runs start, its pipeline |
| [Add a runner](add-runner.md) | a pool, a join token and the install command |
| [Add an environment](add-environment.md) | where deploys may go, and how protected it is |
| [Add a policy](add-policy.md) | rules every run must meet |
| [Add a health source](add-health-source.md) | a metrics or logs system verify steps may read |
| [Add an AI model](add-ai-model.md) | a model steps may ask, and its limits |

## Operate

| Guide | For |
|---|---|
| [Connect a runner](connect-runner.md) | running steps on another machine or cluster |
| [Sign-in with OIDC and roles](oidc-and-roles.md) | turning on sign-in and granting roles |
| [Secret stores](secrets.md) | Vault, OpenBao, files and environment variables |
| [Backup, restore, upgrade](backup-restore-upgrade.md) | keeping data safe, and changing versions |
| [Monitor Launchway](monitoring.md) | health checks, logs and what to alert on |
| [Troubleshooting](troubleshooting.md) | error messages and what to do |
