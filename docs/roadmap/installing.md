# Installing Launchway

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Installing Launchway · <a href="../roadmap-list.html">as a list</a></p>

Installing should be simple, and the same for everyone: open the address, sign in and be guided. **Status:** in progress, in [Milestone 1](milestone-1.md). Nothing is assumed: no secrets manager, no identity provider and no other tool.

## One command, three targets

```mermaid
flowchart TD
  C["launchway install<br/>a few plain questions"] --> L["Laptop<br/>one program, built-in database,<br/>this computer only"]
  C --> S["Single server<br/>a service, your database or ours,<br/>certificate choices"]
  C --> K["Kubernetes<br/>shows the cluster, asks by name,<br/>runs Helm or writes files to commit"]
  L --> B["Open the address in a browser"]
  S --> B
  K --> B
  B --> W["Sign in with the one-time setup code,<br/>then the first-run wizard"]
```

Each target asks the same few questions. The installer is safe to run again and never regenerates keys. It ends by printing the address and the one-time step, then a plain security checklist.

## What the chart does for you

A handful of values starts a secure, working installation. The chart creates the root key, the database password and the first administrator’s setup once and keeps them across upgrades, even when a GitOps controller renders it. It ships default-deny network policies and restricted pods, and nothing is exposed publicly by default.

## The first-run wizard

After the first sign-in, the browser walks through naming your organization, sign-in (keep the built-in accounts or connect a provider, tested before you switch), encryption status and a backup of the root key, a Station (runner), Git hosting, importing repositories and a first pipeline.

## What comes next

A small bundled Kubernetes for a server with none, a check of every cluster feature Launchway needs with a fix or a labeled trial option for each one missing, and an offline bundle for networks with no internet. Anything not built yet is shown as coming, never as a choice that fails.

Go back to the [Roadmap](../public/roadmap.html).
