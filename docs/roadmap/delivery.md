# Delivery: applications, rollouts and hosts

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Delivery · <a href="../roadmap-list.html">as a list</a></p>

Everything after the build: applications in clusters, virtual machines, image-based hosts and clouds, and finding out why something failed. **Status:** deploying with Argo CD, approvals, policy and verification is available today (see [deployments](../guide/deployments.md)); the depth described here is planned in [Milestone 3](milestone-3.md).

## The applications hierarchy

```mermaid
flowchart TD
  Root["Fleet"] --> Env1["Environment: test"]
  Root --> Env2["Environment: production"]
  Env2 --> AoA["App of apps"]
  AoA --> A1["Application"]
  AoA --> A2["Application"]
  AoA --> Set["ApplicationSet"]
  Set --> G1["Generated application"]
  Set --> G2["Generated application"]
  A1 --> R["Workloads, services, settings"]
```

Every level shows status, sync, health, history and actions. Zoom out for the fleet, zoom in for the resources behind one application.

## A rollout, step by step

```mermaid
sequenceDiagram
  participant P as Run
  participant L as Launchway
  participant R as Rollout
  participant W as Watch
  P->>L: promote version
  L->>R: 25 percent to new
  R->>W: errors, speed
  W-->>L: within limits
  L->>R: 50, then 100 percent
  W-->>L: limit crossed
  L->>R: back to old version
  L-->>P: result, evidence
```

## Why did it fail

```mermaid
flowchart TD
  Fail["Deployment failed"] --> Diag["One-sentence diagnosis"]
  Diag --> Ev["Evidence chain<br/>change, new copy, first error, alert"]
  Ev --> Tree["Everything deployed<br/>resources, events, manifests, logs"]
  Diag --> Act["Recommended action<br/>with its consequence"]
```

Each piece of evidence opens the resource where it shows. Secrets appear by name only.

## Machine images and image-based hosts

```mermaid
flowchart TD
  Def["Image definition and stack"] --> Build["Build in a pipeline"]
  Build --> Sign["Scan, sign, publish"]
  Sign --> Stage["Hosts stage the new image"]
  Stage --> Boot["Reboot in batches<br/>check between batches"]
  Boot --> First["First boot: name, network, keys, zone"]
  First --> Serve["Serving, verified"]
  Serve -->|problem| Back["Go back to the previous image"]
```

Hosts are managed like container images, built on bootc. The first-boot step may run a configuration playbook once, before the host takes traffic; running one on a host already serving traffic is refused because that is what brings drift back.

Capabilities: [Milestone 3](milestone-3.md). Go back to the [Roadmap](../public/roadmap.html).
