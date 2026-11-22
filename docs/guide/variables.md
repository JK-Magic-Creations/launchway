# Variables at levels

A **variable** is a named setting with a type and a value, such as a Depot (registry), a number of replicas or a
region. You set it once at a **level**, everything below that level gets it, a nearer level can set the
same name to override it, and a **lock** keeps it from being overridden. You need none of this to run a
pipeline: every template has its own defaults, and a pipeline file can say everything itself.

## The rule

One sentence, the same on every page, in every message and in every interface:

**The nearest setting wins: what is given when a Sortie (run) starts, then the pipeline file's own `with:`, then the Flight (codebase), the Squadron, the Wing, the Base, then the template's own default; at the same level a variable set for the Airfield (environment) being deployed to beats the general one; and a lock set at a level beats everything below it.** Lists replace, never merge. Variables and locks never cross Wings.

The words Squadron, Wing and Base are the default names of the levels; the business set calls them
team, organization and installation, and [Words](words.md) renames any of them. The rule does not change.

If you write code, the analogy is exact. A template's inputs are the **interface**. A variable at a wider
level is an **abstract class** that supplies a value. A nearer level is the **class** that **overrides**
it. A lock is `final`: nothing below may override it.

## Levels

| Level | Reaches | Set by |
|---|---|---|
| Base (installation) | every Flight | an administrator of every Flight |
| Wing (organization) | every Flight of the Wing | an administrator of every Flight |
| Squadron (unit) | the Flights in that Squadron | an administrator of every Flight in the Squadron |
| Flight | that Flight | an operator of the Flight |

A lock needs the level's administrator: of every Flight for the installation and the Wing, of
the Squadron's Flights for a Squadron, of the Flight for a Flight. A variable set for a Squadron, or the
Wing, never reaches another Squadron or another Wing: variables and locks do not cross
Wings.

## Types

| Type | Value | Notes |
|---|---|---|
| `string` | text | up to 4096 bytes |
| `number` | a number | shown in its shortest form |
| `boolean` | `true` or `false` | |
| `list` | text, numbers or true/false | the nearest list is the whole list: lists replace, never merge |
| `map` | names to text, numbers or true/false | |
| `secret` | a reference `{ store, key, field }` | never the secret: a Station (runner) looks it up when a step starts, and only the reference is shown or recorded |
| `connection` | the name of a connection, with `of` (Depot, cluster, ...) | |

A name keeps **one type everywhere**: the first level to set `replicas` as a number fixes it, and
setting it as text at a Flight is refused. A name is lower-case letters, digits and `_`, starting with
a letter, at most 40 characters.

## For an Airfield {#for-an-environment}

A variable may be for one Airfield, such as `production`. Resolved for a deploy to that Airfield,
at the same level the Airfield's variable beats the general one. A Flight's own general setting still
beats the Wing's variable for production: a Flight is in control of itself unless something
above **locks**. A step with no Airfield, such as a build or a test, sees only the general variables.

## Locks

A lock is set on a variable at a level, with a reason that is shown wherever it stops someone. It means
"this is the variable for everything below, and nobody below may change it", a pipeline file and a Sortie
included. Settings below the lock are **kept, not deleted**, and shown as ignored; removing the lock
brings them back. Setting a lock tells you which nearer settings it now ignores. When there are several
locks, the topmost wins, and an Airfield's lock beats a general lock at the same level. A locked
secret reference is allowed: nobody below may point elsewhere.

## Using a variable in a pipeline

A step does not receive every variable: it receives what the pipeline names. An input of the pipeline,
or a parameter of a template, takes a variable with `from:`:

```yaml
inputs:
  region: { type: string, from: region, default: us }   # the variable, else us
```

```yaml
template: service-cd
params:
  registry: { type: string, from: registry }            # the variable, unless a use writes with:
  replicas: { type: number, from: replicas, default: 1 }
```

The binding is explicit: a variable that happens to have the same name reaches nothing. The value comes
before the input's or parameter's own default, and after what is given when a Sortie starts and what a use
writes under `with:`. A pipeline whose input `environment` is given or defaulted resolves the
variables for that Airfield.

When a lock stands on the variable, the file and the Sortie cannot override it:

```text
registry is locked to harbor-retail by the wing (everything ships through the audited registry); the pipeline file sets harbor-cache. Only an administrator of every Flight can change it.
```

This is reported by `launchway validate --project`, the editor, and when a Sortie is created, before any
Station. Giving the locked value itself is accepted. If a template needs a variable and nothing sets it, the
message says where to set it: for the Flight, its Squadron or its Wing (Settings, Variables), or under `with:`.

## What a Sortie used {#what-a-run-used}

A Sortie records the variables it used, each with the whole chain at that moment, when it is created. The
run page shows them under "Variables in effect"; a **re-run** uses the recorded ones, so it repeats
exactly, and changing a variable later does not change a Sortie that exists. A secret is recorded as its
reference.

## Where to see and change them

- **Settings, Variables**: the variables at each level, with what each inherits from above.
- **A Flight, Variables**: everything in effect for the Flight, each with where it comes from, the
  values it overrides struck through in order, and a lock badge saying who locked it. **Override here** is
  one click, and disabled with the reason when a lock above forbids it.
- The same panel sits beside the pipeline editor and in the run-start form, so the hierarchy is visible
  while you build a pipeline or start a Sortie.
- `launchway variables ...`, the [HTTP API](../reference/http-api.md#variables) and the MCP tools do the
  same. Each change is in the [audit trail](../reference/events.md) as `variable.set`, `variable.delete`,
  `variable.lock` or `variable.unlock`, and each variable keeps its own history: who, old and new value,
  a secret only as its reference.

```text
launchway variables set registry --organization --type connection --of registry --value harbor-retail
launchway variables lock registry --organization --reason "Everything ships through the audited registry"
launchway variables set replicas --project cart-api --environment production --type number --value 3
launchway variables effective cart-api --environment production
```

## What is not built yet

Variables bound beyond `from:` on inputs and parameters (the `{ variable: name }` form inside `env:` and
`with:`), templates that a level requires, connections as objects of their own, and more than one
Wing are still to come; so is a Terraform resource (it is planned, see the
[parity table](../reference/interface-parity.md)).
