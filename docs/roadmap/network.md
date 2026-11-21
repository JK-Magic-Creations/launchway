# Network and traffic

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Network and traffic · <a href="../roadmap-list.html">as a list</a></p>

For every application: can it receive and serve requests right now, and if not, at which step does the route break? **Status:** planned, in [Milestone 9](milestone-9.md). The live traffic map and protection views are in [Milestone 3](traffic.md).

## The path of a request, and where findings come from

```mermaid
flowchart TD
  U["Users"] --> E["Edge provider"]
  E --> D["DNS"]
  D --> G["Gateway or load balancer"]
  G --> F["Firewall and network policy"]
  F --> S["Service"]
  S --> W["Workloads and machines"]
  W --> X["What the app calls"]
  Src["Sources you allow<br/>metrics, flows, mesh, probes"] -.-> Fi["Findings, worst first"]
  Fi -.-> G
  Fi -.-> F
  Fi -.-> W
```

Each step is marked healthy, broken or not observed. Findings come from the sources you have given Launchway access to, and a step with no source is dotted and labeled, never given an invented number.

## What it sees, and what it never does

Launchway sees addresses, names, ports, protocols, counts and timings. It never captures what the traffic contains. Everything is optional: with no source it still draws the route you declared, and every extra permission shows what it would add.

## DNS as part of a deployment

```mermaid
flowchart TD
  Decl["Deployment declares<br/>the records it needs"] --> Diff["Show the difference"]
  Diff --> Clear["Cleared by a person"]
  Clear --> Apply["Create records"]
  Apply --> Check["Check every nameserver agrees"]
```

Launchway writes only records a deployment declares and owns, through a connection limited to one zone, and never changes firewalls, DHCP, resolver settings or records it does not own.

## Service meshes

```mermaid
flowchart TD
  Req["A request"] --> VS["Routing rules<br/>first match wins"]
  VS --> Sub["Subset of workloads"]
  Sub --> Proof["Per-subset proof"]
```

Istio is read most deeply: its routing rules are explained so you can see which one matches first, and a route for a subset of clients is previewed before it is applied. A mesh managed by Tetrate is read at both of its layers.

Go back to the [Roadmap](../public/roadmap.html).
