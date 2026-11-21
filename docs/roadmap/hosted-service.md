# The hosted service and sandboxed runners

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Hosted service · <a href="../roadmap-list.html">as a list</a></p>

Launchway as a service, with the controls enterprises and regulated teams ask for. **Status:** planned, in [Milestone 7](milestone-7.md). A local demo already runs from the [quick start](../start/quick-start.md).

## Two ways to run steps

```mermaid
flowchart TD
  Team["Your team"] --> Org["Your organisation<br/>isolated from others"]
  Org --> Eng["Hosted engine"]
  Eng --> Sb["Sandboxed hosted runners"]
  Own["Runners on your machines"] -->|"outbound connection only"| Eng
```

With runners on your own servers or clusters, code and secrets stay inside your network.

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

Organisations are isolated and tested for it. Directory groups, built-in accounts with passkeys, a tamper-evident audit trail, retention and legal hold, encryption at rest and a PCI DSS and HIPAA evidence pack are in the same milestone. Launchway supports compliance work; it is not a certification.

Go back to the [Roadmap](../public/roadmap.html).
