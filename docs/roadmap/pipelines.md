# Pipelines and flow control

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › Pipelines · <a href="../roadmap-list.html">as a list</a></p>

Pipelines live in Git next to the code they build. Launchway reads the pipeline at the commit a run is for. Flow control, caches and many triggers are **available today** (see the [pipeline reference](../reference/pipeline.md)); custom steps, generated pipelines, expressions, matrices and visual design are planned in [Milestone 1](milestone-1.md#g-pipelines-that-match-what-teams-expect-from-hosted-ci) and [Milestone 2](milestone-2.md).

## How a run is planned and ordered

```mermaid
flowchart TD
  T["Trigger<br/>push, pull request, schedule, queue"] --> L["Read the pipeline at that commit"]
  L --> C["Check: schema, types, policy"]
  C -->|fails| X["Refused before any step runs"]
  C --> Q["Lane: wait your turn"]
  Q --> G["Run graph"]
  G --> S1["Step"]
  G --> S2["Step"]
  S1 --> J["Join"]
  S2 --> J
  J --> R["Results, reports, artifacts"]
```

Everything that can be checked is checked before the run, including generated steps and references to outputs. A run in a lane waits for the one ahead.

## The life of a step

```mermaid
stateDiagram-v2
  [*] --> Waiting
  Waiting --> Skipped: condition false
  Waiting --> Running: ready
  Running --> Passed
  Running --> Retrying: failed, retries left
  Retrying --> Running: after back-off
  Running --> Failed
  Failed --> Running: re-run from here
  Passed --> [*]
  Skipped --> [*]
```

Re-running from a step gives it exactly what it was given before, so a fix does not mean starting over.

## Custom and generated steps

```mermaid
flowchart LR
  P["Program in your repository<br/>typed inputs, outputs, secrets"] --> Test["Tested on its own"]
  Test --> Pin["Published and pinned by tag"]
  Pin --> Use["Used by many pipelines"]
  Gen["A step that prints more steps"] --> Check["Same schema and policy check"]
  Check --> Show["Shown on the run"]
```

A template is an interface and code is what runs. Because expressions have no loops and no side effects, a pipeline can always be checked without running it.

## Where each idea is listed

[Flow control, caches and matrices](milestone-1.md#g-pipelines-that-match-what-teams-expect-from-hosted-ci), [custom steps and generated pipelines](milestone-2.md#g-write-pipelines-your-way), [visual design](milestone-2.md#g-design-visually), and [moving off an automation server](milestone-2.md#g-move-off-a-self-hosted-automation-server).

Go back to the [Roadmap](../public/roadmap.html).
