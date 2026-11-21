# The hosted service and sandboxed runners

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Hosted service · <a href="../roadmap-list.html">as a list</a></p>

Launchway as a service, with the controls enterprises and regulated teams ask for. **Status:** planned, in [Milestone 7](milestone-7.md), which also covers a protected demonstration that never shows the operator's own systems. A local demo already runs from the [quick start](../start/quick-start.md).

## Two ways to run steps

```mermaid
flowchart TD
  Team["Your team"] --> Org["Your organisation<br/>isolated from others"]
  Org --> Eng["Hosted engine"]
  Eng --> Sb["Sandboxed hosted runners"]
  Own["Runners on your machines"] -->|"outbound connection only"| Eng
```

With runners on your own servers or clusters, code and secrets stay inside your network.

## What stays with you, and what never crosses

```mermaid
flowchart TD
  subgraph H["Hosted service"]
    Acc["Accounts and billing"]
    Conf["Configuration and approvals"]
    Coord["Coordination and status"]
  end
  subgraph C["Your side: the Detachment"]
    Run["Runners"]
    Sec["Your secret store"]
    Net["Your clusters, metrics, logs, DNS"]
    Keep["Your logs and audit"]
  end
  C -->|"calls out only"| H
  Never["Never crosses: secrets, traffic,<br/>logs, code, database contents"] -.-> H
```

The hosted service is the control plane: it remembers who you are, what you have configured and what should happen next. Your side does the work and holds the data. It sees names, states, counts and timings, and the pages you read are drawn in your browser from data fetched live from your Detachment. It has no way to open a connection into your network.

## Isolation layers for hosted steps

```mermaid
flowchart TB
  subgraph L1["Layer 1: the boundary"]
    VM["One isolated machine per run"]
  end
  subgraph L2["Layer 2: the network"]
    Net["No route to anything but the internet<br/>own source address, kill switch"]
  end
  subgraph L3["Layer 3: the supply"]
    Img["Images fetched through a checking proxy"]
  end
  subgraph L4["Layer 4: limits"]
    Q["Firm quotas: processors, memory, disk, time, bandwidth"]
  end
  subgraph L5["Layer 5: watching"]
    W["Metering, alerts, audit"]
  end
  L1 --> L2 --> L3 --> L4 --> L5
```

Each layer assumes the one before it can fail. The same profile is available to any self-hosted server that wants to run untrusted work.

## Identity and audit

Organizations are isolated and tested for it. Directory groups, built-in accounts with passkeys, a tamper-evident audit trail, retention and legal hold, encryption at rest and a PCI DSS and HIPAA evidence pack are in the same milestone. Launchway supports compliance work; it is not a certification.

Go back to the [Roadmap](../public/roadmap.html).
