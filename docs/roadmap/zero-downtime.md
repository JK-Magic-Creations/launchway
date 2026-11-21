# Zero-downtime delivery

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Zero-downtime delivery · <a href="../roadmap-list.html">as a list</a></p>

Every deployment is the same journey: bring the new version up beside the old one, prove it, move traffic or work over in steps you can see and control, and keep the way back open until you are sure. **Status:** planned, in [Milestone 8](milestone-8.md). Today Launchway reads rollouts, watches them, and a failed command says so; the pieces below are built on that.

## One model for every deployment

```mermaid
flowchart TD
  Plan["Plan<br/>what to roll out"] --> Shifter["Shifter<br/>moves traffic or work"]
  Shifter --> Proof["Proof<br/>is it safe?"]
  Proof -->|yes| Shifter
  Proof -->|worse| Return["Return"]
  Proof -->|done| Watch["The watch"]
  Watch -->|steady| Stable["Stable state"]
  Watch -->|worse| Return
  Return --> Stable
  Control["Controls<br/>pause, go, stop"] -.-> Shifter
```

A web shop on Kubernetes, a service on four virtual machines, a Holding pattern (queue) worker and a nightly job all use this picture. Only the shifter changes: a traffic split, a load balancer pool, which copies read the Holding pattern, or a function alias.

## The watch after a deployment

```mermaid
sequenceDiagram
  participant R as Release
  participant W as Watch
  participant S as Stable state
  R->>W: last step reached
  W->>W: compare with how it normally behaves
  W-->>R: steady for the whole time
  R->>S: record as stable
  W-->>S: signals worse
  S-->>R: return to last stable
```

The watch carries on for a time you choose. If error rates or other signals worsen, Launchway returns to the last stable state. A toggle that caused the errors is switched back first.

## Levers that change behavior without a build

```mermaid
flowchart TD
  Toggle["Toggles"] --> State["Stable state"]
  Config["Configuration"] --> State
  Secret["Secret versions"] --> State
  State --> Timeline["One timeline of what changed"]
```

Toggles, configuration and secret versions are rolled out and returned like a release, and appear on one timeline.

## What it will not do

Launchway cannot make a change that is not backward compatible safe, and a rollout alone cannot give a whole system full nines. A proof is only as good as its signal: when there is too little data it says not enough data rather than pass. DNS cannot shift traffic quickly or exactly. Some work cannot overlap, and for that the honest answer is a short controlled pause.

Go back to the [Roadmap](../public/roadmap.html).
