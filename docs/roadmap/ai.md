# AI as an optional helper

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › Areas › AI · <a href="../roadmap-list.html">as a list</a></p>

AI wherever it saves effort, always optional, and never able to hide a failure. **Status:** models in steps and agents as users are available today ([AI models](../guide/ai.md), [agents](../guide/agents.md)); the gateway and the rest are planned in [Milestone 6](milestone-6.md).

## The path of every AI call

```mermaid
flowchart TD
  Ask["Step, agent or screen"] --> Sw{"Purpose switched on?"}
  Sw -->|no| Stop["Nothing is sent"]
  Sw -->|yes| Cls{"Regulated project?"}
  Cls -->|yes| Safe["Only models with recorded data terms"]
  Cls -->|no| Comp["Compress context<br/>keep paths, lines, codes"]
  Safe --> Comp
  Comp --> Bud{"Within budget?"}
  Bud -->|no| Stop2["Stop or warn"]
  Bud -->|yes| GW["Model gateway"]
  GW --> Local["Local or self-hosted"]
  GW --> Host["Hosted provider"]
  GW --> Rec["Usage and audit record"]
```

## Rules that do not bend

- Off until an administrator turns a purpose on.
- A model's verdict never overrides a failing check. AI analysis can trigger a rollback only where a verification step allows it, and production can require a person.
- Everything AI produced is labelled, and AI never approves or merges on its own.
- Local models are first-class, for data that cannot leave the building.
- Spending limits are in money, per model, project and purpose.

Go back to the [Roadmap](../public/roadmap.html).
