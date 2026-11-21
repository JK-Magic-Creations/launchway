# The software graph and automated patching

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Software graph · <a href="../roadmap-list.html">as a list</a></p>

Launchway already records the facts needed to answer what is running, where, built from what, containing what, and is any of it vulnerable. The graph keys those facts by image digest and by deployment so one question can span code, packages, images and deployments. **Status:** findings and bills of materials per Sortie (run) are available today; the graph and patching are planned in [Milestone 5](milestone-5.md).

## What is connected

```mermaid
flowchart TD
  Repo["Repository, commit, file, symbol"] --> Run["Run and step"]
  Run --> Img["Image, chart, OS image<br/>by digest"]
  Pkg["Package, version, licence"] --> Img
  Img --> Dep["Deployment"]
  Dep --> Place["Cluster, namespace, host, zone"]
  Adv["Advisory, vendor statement"] --> Pkg
  Owner["Owner, team, work item"] --> Repo
  Owner --> Dep
```

## The patching loop

```mermaid
sequenceDiagram
  participant A as Advisories
  participant G as Graph
  participant C as Change and tests
  participant R as Approval and rollout
  A->>G: new advisory
  G->>G: find affected software
  G->>C: open a change with the update
  C-->>C: tests pass
  C->>R: ask approval, check freezes
  R->>R: roll out, verify
  R-->>G: record the deployment
```

Automated patching is off by default and turned on per zone. Every step is an ordinary, audited change, and a person can be required for production.

## Questions it answers

- Which services run this library, at what version, and who owns them?
- Which running image has this vulnerability, and is a fix available?
- What commit, run and image is this deployment, including on hosts Launchway only observes?
- Is every production host patched inside our own deadline?

Go back to the [Roadmap](../public/roadmap.html).
