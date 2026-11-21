# Configuration and levels

Launchway keeps everything an administrator manages in its database, not in a file. This page
explains what that means, why, how it fits teams that want configuration in Git, and how the
levels (Base, Wing, Squadron, Project) work. For the settings themselves, see
[Configuration objects](../reference/config-objects.md).

## Why the database

A configuration file that has to be edited, committed, deployed and loaded before anything
changes is slow to change, easy to get wrong, and hard to audit. Launchway keeps its
configuration as objects in its database instead:

- **Every change is checked before it is stored.** The checks are the ones the old configuration
  file was held to, so a mistake is refused with every problem listed, not found when a server
  restarts.
- **Every change has a history.** For each object: who changed it, when, what the new settings are
  and what changed. A change can be undone by restoring an earlier version, and a deleted object
  can be restored.
- **Changes apply at once, everywhere.** Servers that share a database see a change within
  seconds, with no restart. Two people cannot overwrite each other: a change names the version it
  was made from, and is refused if the object has moved on.
- **One door for people, scripts and agents.** The web UI, the `launchway` command, the HTTP API,
  the MCP server for agents and (planned) a Terraform provider all call the same API, with the
  same permissions and the same checks. See [Interface parity](../reference/interface-parity.md).

## What is not in the database: pipelines

Pipelines, templates, shared libraries and custom step code are code, and **stay in Git**, the
single place they exist. Launchway reads a pipeline from the repository at the commit a run is for, so a
feature branch runs its own version of it and a pull request shows the pipeline change beside the code
change. The database holds only *settings about* them: a project's configuration is the reference to its
repository (address, default branch, how runs start) and, for a project that uses a template, which
library, which template and the version it is pinned to (a tag, a branch or a commit). There is no
copy of a pipeline in the database to keep in step with Git. Saving from the browser editor is a Git
commit: to a new branch by default, or directly where the project allows it. One connection to a Git
host (the server's own credentials) covers every repository it may read; nothing is connected per
repository.

## What stays in the file

The file a server starts with holds only what it needs before it can read the database:

- the database connection (`database`, `database_wait`) and the data directory (`data`);
- where it listens (`listen`) and whether a proxy is in front (`server.trust_proxy`, `server.drain_delay`);
- how people sign in (`auth.mode`, `auth.oidc`, the session timeouts) and who the first
  administrators are (`auth.admins`), because sign-in must work before anyone can change anything;
- what runs on the server's own machine (`runner`), who actions are recorded as without sign-in
  (`user`), and `reports.origin`, the address the server is reached at.

Everything else lives in the database: projects, libraries, environments, runner pools, role
bindings, policies, health sources, AI models, secret-store definitions, limits and retention, the
MCP endpoint, and where artifacts are kept. The [server configuration reference](../reference/server-config.md)
marks each setting.

Sign-in stays in the file for a reason: a server cannot ask the database "who may sign in" before
it knows who is asking. The setup wizard asks the questions, tests your answers against the
identity provider, and writes the few lines for the file; it never asks for the client secret,
which a server reads from an environment variable.

## The first start: one import, then the database decides

If a server of this release starts against a database that has no configuration yet, and its file
holds settings that now live in the database (a project list from an older installation, say), it
**imports them once**. The import is recorded as an event you can read (`launchway config imports`),
with what it brought in. From then on the database is the only source of truth: the file's copies
of those settings are **ignored**, and each server says so when it starts, naming every ignored key
and the import:

```text
the configuration file sets projects, environments, runner_pools; these live in the database since the import
imp_a1b2c3 and are ignored; change them with the web UI, launchway config, the API or Terraform
```

Starting a second server, or restarting one with a different file, imports nothing.

## Configuration as code

Teams that want their configuration in Git keep it there and apply it on purpose:

```sh
launchway config export -o launchway-config.yaml     # backup, review, commit
launchway config import launchway-config.yaml        # shows what would change
launchway config import launchway-config.yaml --apply
```

`import` always shows the plan first; `--apply` makes exactly the changes the plan listed, as one
recorded import. `--prune` also deletes objects the file does not have. This is a way to *change*
the database, not a second source of truth at runtime: nothing reads the file while the server runs.
A Terraform provider is planned on the same API; objects it manages are marked, and the UI warns
before you edit one by hand.

## Levels

Launchway has four levels, from the whole installation down to one repository. The table uses the default words; the plain business set calls them Installation, Organization, Team and Project ([the words Launchway uses](words.md)).

| Level | What it is | Shown |
|---|---|---|
| **Base** | Everything on this installation: limits, retention, the words used | always, as settings |
| **Wing** | The one that owns the projects. There is one today | only where it helps |
| **Squadron** | An optional group of projects with shared settings: a team, a product line, a department. One level, never nested | only after the first squadron is created |
| **Project** | One repository and its pipeline | always |

Objects that can be defined at a level apply to the projects below it. A **role binding**, a
**library**, a **policy**, an **environment**, a **runner pool**, a **health source**, an **AI model**
or a **secret store** defined at a Squadron applies to the Squadron's projects; defined at the Wing it
applies to every project. A project can add to what it inherits, or, where a setting allows it,
change it. The project page shows where each effective value comes from (`launchway config effective
PROJECT` prints the same).

The **words** for the levels are yours to choose: pick the plain business set, or rename the Wing
"Company", the Squadron "Department" or "Squad", the project "Service" or "Repository". Words are
changed on **Settings → Words** (see [the words Launchway uses](words.md)) and are labels only: the
configuration kinds keep their names (`organisation`, `unit`), so files and scripts do not change.
Levels nobody uses stay hidden: with no squadron there is no squadron column, picker or page.

There is exactly one Wing today. The tables carry an `org` column in their keys, so
several wings can be added later without rewriting them; nothing isolates
wings from each other yet, and this release does not claim to.

## Upgrading and rolling back

See [Backup, restore, upgrade](../howto/backup-restore-upgrade.md#configuration-in-the-database).
In short: servers of the release before keep using their own file until they are replaced, the import
changes nothing they read, and to go back you export, roll the servers back and give them the
exported text as their file.

## Guided setup

Each setup task has a wizard in the web UI that asks plain questions, explains what each option does,
checks your answers with the same rules as the API, shows exactly what it will create, and creates
it all at once or not at all:

- [First-run setup](../howto/setup-first-run.md)
- [Connect a repository](../howto/connect-repository.md)
- [Add a runner](../howto/add-runner.md)
- [Add an environment](../howto/add-environment.md)
- [Add a policy](../howto/add-policy.md)
- [Add a health source](../howto/add-health-source.md) and [add an AI model](../howto/add-ai-model.md)
