# Write your own step

Built-in steps cover the common work. When you need something unique, write the step as ordinary code in a
language you already use, test it on its own, use it in a pipeline and share it in a library. This takes about
fifteen minutes. You need Launchway's command line, and Podman or Docker if you want to see the sandbox.

## 1. Scaffold

```sh
launchway step new --lang python --name greet     # or go, ts, sh
```

This writes `steps/greet`: `step.yaml` (the manifest), `main.py`, `launchway_step.py` (the kit, a small
file with no dependencies), `test_main.py` and a README. The same layout holds for the other languages
(`main.go` and `lwstep/`, `main.ts`, `main.sh`).

## 2. Edit

The manifest says what the step takes and gives:

```yaml
step: greet
contract: 1
toolchain: python@3.13
command: python3 -B main.py
inputs:
  who: { type: string, default: world, pattern: "^[A-Za-z][A-Za-z ]{0,39}$" }
outputs:
  greeting: { type: string }
```

The code reads inputs, does the work and reports through the kit:

```python
def main(step):
    g = greet(step.string("who"))
    step.output("greeting", g)
    step.summary("Greeted **%s**" % step.string("who"))
```

`step.annotate(...)` puts a note on a line of a file, `step.finding(...)` adds a finding and `step.test(...)`
a test result. A step fails by raising `Failed("why")`. The [contract](../reference/step-contract.md) is all
there is to the kit; a language without one implements it in a few lines.

## 3. Test on your own machine

```sh
launchway step test steps/greet --with who=Ada
```

runs the step as a Station would: in its image, with no network, a read-only workspace and only the secrets
it names. It shows the outputs and results and checks that the step keeps to the contract (outputs of the
declared types, results on the right channel, sensible exit codes). The code under test is the same
function your unit test calls: `python3 -B -m unittest` (or `go test`, `node --test`, `sh test.sh`).

## 4. Use it in a pipeline

In the same repository:

```yaml
steps:
  greet:
    uses: ./steps/greet
    with: { who: Ada }
```

`launchway validate` expands it and prints what it may do: "This step can reach no network, read no secrets and
write only its own output files." A wrong `with` (an unknown input, a value of the wrong type, a missing required one) is an
error on the line of the value, before anything runs. Later steps get its outputs as
`LAUNCHWAY_NEEDS_GREET_GREETING`, with their declared types (a list or record as JSON text).

## 5. Share it

Put the `steps/greet` directory in a [library](libraries.md) (a Git repository), tag a release and use it:

```yaml
libraries:
  platform: { repo: tools, version: v1 }
steps:
  greet: { uses: platform/greet }
```

A tag or a commit never changes under a pipeline. The library's step page (`launchway steps get platform greet`,
or the catalogue in the web interface) lists its inputs and outputs, what it may do, its versions and who uses
it. When a newer tag changes what the step may do, the page and `launchway step validate --against v1` say so
before anyone updates: "permission: the step may now reach registry.example.org".

## Permissions

A step asks for what it needs in the manifest, and a person adding it sees that in one sentence:

```yaml
permissions:
  network: [registry.npmjs.org]
  write: [dist]
```

See [what each executor enforces](../reference/step-manifest.md#what-a-step-may-do). Secrets are inputs of
type `secret`, given by reference (`token_store`, `token_key`) and masked in the log.

## Safety of results

Launchway reads results only from the file the Station gives the step, never from its output, so no line a
build tool prints can forge a finding or an output. Start tools you do not trust through the kit's
`environ()` (Go `lwstep.Environ()`, shell `lw_run_tool`), which leaves out the variables that let them write
results. See [the trust boundary](../reference/step-contract.md#the-trust-boundary).
