# The step manifest

`steps/<name>/step.yaml` says what a [custom step](../guide/write-your-own-step.md) is, takes, gives and
may do. It is read before a Sortie starts, so a pipeline that uses the step is checked against it, and what
it asks for is shown to the person who adds the step.

```yaml
step: notes                      # the name; the directory is steps/notes
contract: 1                      # the version of the step contract it speaks
description: Write release notes
toolchain: python@3.13           # or image: docker.io/library/python:3.13-slim
command: python3 main.py         # one shell line, or a list of arguments
inputs:
  title: { type: string, description: The title, pattern: "^[A-Za-z ]+$", max: 40 }
  count: { type: number, default: 3, min: 1, max: 10 }
  files: { type: list, default: [a, b] }
  token: { type: secret }
outputs:
  url: { type: string }
  total: { type: number }
resources: { cpu: 500m, memory: 256Mi }
timeout: 10m
permissions:
  network: [registry.npmjs.org]  # hosts it may reach; none by default
  write: [dist]                  # workspace paths it may write; none by default
```

| Key | Description |
|---|---|
| `step` | The name: lower-case letters, digits and `-`, at most 63. It is the directory's name |
| `contract` | The contract version, required |
| `description` | One line of at most 300 characters |
| `image` or `toolchain` | The image the step runs in, or `go`, `python`, `node` or `sh`, optionally with `@version` |
| `command` | What to run, in the step's directory |
| `inputs` | A mapping of names (lower case, digits, `_`) to inputs |
| `outputs` | A mapping of names to outputs: `type`, `description` |
| `resources` | `cpu` and `memory`, as for a step's [`resources`](pipeline.md) |
| `timeout` | Per attempt, such as `90s`, `10m` or `1h` |
| `permissions` | What the step may do: `network` and `write` |

An **input** has a `type` (`string`, `number`, `boolean`, `list`, `record` or `secret`), a `description`, a
`default` (an input without one is required, unless it says `required: false`), `options`, and for strings a
`pattern` (RE2) and a length range, for numbers a range, and for lists a number of entries (`min`, `max`). A secret has none of those.
A pipeline gives a secret as `<name>_store`, `<name>_key` and, for a store with fields, `<name>_field`; the
Station looks the value up and puts it in an environment variable named in the inputs file. A step receives
only the secrets it names.

## What a step may do

A step runs in a **sandbox** and the manifest asks for what it needs, nothing more:

| Permission | Default | What is enforced |
|---|---|---|
| `network` | none | Podman and Docker: `--network none` unless hosts are listed. Kubernetes: the pod is labelled so that the chart's network policy gives it no egress at all (not even DNS), or, when hosts are listed, what `networkPolicy.steps.customHosts` allows. A list of hosts is **not** enforced by name on either: Podman cannot filter by name and a policy has addresses, so the list is recorded, shown and audited, and the operator's policy decides. The host executor enforces nothing and refuses the step |
| `write` | none | Podman and Docker: the workspace is read-only except the listed paths and the step's own directory. Kubernetes: the list is recorded, and the workspace is one volume the pod may write (not enforced). A listed path that is a link is refused |
| secrets | only inputs of type `secret` | The step's variables hold exactly those secrets |
| root filesystem | read-only | Podman, Docker and Kubernetes. A private `HOME` and `/tmp` are writable |
| capabilities | none | All dropped, no new privileges (Podman, Docker, Kubernetes) |

The **host executor** runs the step as a process of the Station. It cannot enforce any of this, so it
refuses a custom step unless the operator sets `allow_unsandboxed_steps: true` on the Station, which then
says in the step's log that the sandbox was not enforced.

A step from a library cannot widen its own permissions: they are read from the manifest at the pinned tag or
commit when the Sortie is created and recorded with it, and the Station refuses to give a step a secret its
manifest does not name. A change between versions in what a step may do is called out wherever the step is
shown: `launchway steps get`, the step's page and `launchway step validate --against`.

## Used in a pipeline

```yaml
libraries:
  platform: { repo: tools, version: v2 }   # a tag or a commit: it never changes under the pipeline
steps:
  notes:
    uses: platform/notes                   # or ./steps/notes for a step of this repository
    with: { title: Release, count: 5, token_store: vault, token_key: apps/shop/token }
```

Every use of a step is recorded on the Sortie (the step, its library, the commit and the manifest's digest),
for audit and for the software graph.
