# Feature toggles

A **toggle** is a value your application reads while it runs, so that a feature can be turned on, off or
rolled out to a share of your users without a deployment. Launchway keeps toggles, changes them under
safeguards, records every change, and hands them to your applications through small libraries (see
[Toggle SDKs](toggles-sdks.md)) or a plain HTTP protocol (see the [protocol](../reference/toggles-protocol.md)).
You need none of this to run a pipeline. This page describes what is built today; what is not is at the end.

## The model

A toggle belongs to a project (or is shared from a unit, the organization or the installation, the same
levels [variables](variables.md) have: a nearer level overrides, a lock keeps it from being overridden).
It has:

| Part | What it is |
|---|---|
| **Key** | Lower-case letters, digits and `- _ .`, such as `checkout-v2`. Unique in a project. |
| **Type** | `boolean`, `string`, `number` or `json`. Every value a toggle serves has its type. |
| **Kind** | `release`, `kill` (a kill switch, with a safe value), `experiment`, `operational` or `permission`. |
| **Default** | What it serves while it is off. A toggle starts off in every environment. |
| **Per-environment state** | On or off, prerequisites, rules in order, and what everyone else gets. |
| **Lifecycle** | An owner, an expected removal date, the change that made it, and notes when it may be finished with. |

### Rules

In an environment where a toggle is on, a context (a **key** that names who or what is asking, and
attributes such as `plan` or `app_version`) is decided like this:

1. If the toggle is off, it serves its off value.
2. If a **prerequisite** is not met (toggle A needs toggle B to serve a value), it serves its off value.
3. The **rules** are tried in order; the first whose conditions all match decides.
4. Otherwise everyone else gets the **fallthrough**.

A rule, and the fallthrough, serve **one value** or a **percentage**: a share of keys get one value and the rest another.

A condition tests one attribute of the context: *is*, *is one of*, *starts with*, *ends with*, *contains*,
*is present*, *is in the segment*, a **version** comparison (`semver_eq`, `_lt`, `_lte`, `_gt`, `_gte`, by
semantic versioning, so `2.10.0` is above `2.9.0`), or a **number** comparison. A condition on an attribute
the context does not have is false, and a *not* turns it into true. A **segment** is a named, reusable set of
rules (and, if you must, lists of keys) that conditions can name.

### Percentages are sticky

A percentage is taken over the context's key (or another attribute you name). Each key is hashed into one
of 10,000 buckets: **SHA-256 of the toggle's salt, a dot and the key; the first four bytes as a big-endian
number; modulo 10,000.** A rollout lays its shares end to end over the buckets. So the same key always lands
in the same bucket, on every server and in every SDK, and raising a rollout from 5% to 25% keeps everyone who
already had it. Each toggle has its own salt, fixed when it is made (and kept if the toggle is deleted and
restored), so two toggles do not put the same people first. Changing a salt would reshuffle everyone, and
nothing in the interface does that. Use text for keys: a number is hashed as its digits.

## Safeguards: on by default, the team in charge

| Safeguard | Default | What it does |
|---|---|---|
| **A reason** | on, everywhere | A change needs a sentence saying why. |
| **An approval** | on in a **protected** environment | A change waits until someone other than the person who asked approves it. The environment's reviewers, if it names some, decide who; `approvals.allow_self` lets a person approve their own. A number of approvals may be asked. |
| **A watch** | off | For some minutes after a change the environment is marked as watched, on the toggle's page, the timeline and the rollback page. |

A change that waits has not happened: the toggle serves what it served. A **kill switch** turned to its safe
value is never held for an approval, and neither is any **revert**: going back is not gated (it still needs a
reason, and is recorded).

A team can **turn a safeguard down** at its level, for one environment or all; that needs a reason and is
recorded in the toggle history and the audit trail. A level above can **lock** a safeguard, and that is the
only way a team loses the choice. The settings page of a project's toggles shows each safeguard and which
level set it. Launchway never rewrites your application: where it has advice, it advises.

## History and revert

Every change records who, when, why, how (web page, CLI, MCP tool, API, a pipeline, a file, a schedule, a
rollback), what it was before and after, and the approvals. **Revert** on any change puts the environment (or
the definition, or a deleted toggle) back as it was before that change; it is a new change and can itself be
reverted. If later changes were made meanwhile, the answer says how many the revert also undid.

## Scheduled changes

Give a time when changing a toggle and it is made then, by the servers, once (two servers never make the
same one). The safeguards apply when it runs: a change that needs approval is made as a waiting change then,
and the schedule says so.

## What changed before it broke

Every toggle change in an environment is on that environment's page, **beside its deployments**, newest first.
The **rollback** page lists the toggle changes made in the environment since the version it would restore
went live. A rollback restores the version only; so the confirmation offers to **revert those toggle changes with
it** (unchecked by default: that is your decision), and the environment page can revert them on their own. The
toggles are reverted after the rollback has been accepted; if one cannot be reverted, the answer names it and the
rest go on.

## Stale toggles: a nudge, never an action

A toggle that has served one value to everyone in every environment for 30 days, has not been evaluated for 30
days by any application that reports, or is past its expected removal date, is mentioned on its page and in the
list. Nothing is removed, turned off or changed by it. The way to finish with a toggle is to remove the check
from your code and then delete the toggle (the deletion can be reverted).

## Keys for applications

An application reads toggles with an **SDK key** of one environment of one project, which can only read:

| Key | Looks like | Use | Can |
|---|---|---|---|
| **Server key** | `lwt_srv_...` | A service you run | Download the ruleset and evaluate locally; open the stream; report counts; evaluate remotely. It can read every rule, so it is a **secret**. |
| **Client key** | `lwt_cli_...` | A web page or an app | Ask for the values, for a context it sends, of the toggles you marked **visible to client applications**. Never a rule, a segment or another toggle. Public by nature; may be bound to the websites allowed to use it. |

A server key is shown once, when made, and stored only as a SHA-256 hash. Rotating makes a new key and lets the
old one work for an overlap (a day unless said) so no deployment is caught between them; revoking ends a key at
once. Needs the admin role on the project. A toggle that gates something dangerous must not be client-visible:
**a client-visible toggle's value is public**, and toggles are not a security boundary. A client-visible toggle
cannot have a prerequisite that is not visible.

Rate limits: per key, and an address that sends many keys that are not valid is refused for a minute.

### What is stored about evaluations (the privacy position)

Evaluating a toggle **in your application** sends nothing about the context anywhere. The SDKs report, once a
minute, **counts**: how many times each toggle served each value, with the reason. Remote evaluation (a client
key, or the toggle page's "try a context") sends the context to your own Launchway server, over TLS, which
**does not store, log or audit it**. What is stored is a count per toggle, environment, day, value and reason:
no key, no attribute, no address. A segment that lists keys stores those keys: they can be personal data, so
prefer rules on attributes.

## The toggles file (config as code)

`launchway toggles export PROJECT -o .launchway/toggles.yaml` writes a project's toggles as a file (the same
model as the API, in YAML); you review changes to it in a pull request. `launchway toggles plan PROJECT -f FILE`
shows how the file and the live toggles differ: what to add, what to change, and what exists only live.
`launchway toggles apply PROJECT -f FILE --reason "..."` makes the live toggles match the file, through the
same safeguards (a toggle that needs approval waits). It **never removes** a toggle, an environment or a
segment the file leaves out: those are listed as *only live*. Nothing applies by itself. The web page of a
project's toggles has the same three steps in "The toggles file".

Launchway does not read the file from your repository for you yet: run `plan` in the pull request's pipeline to
show the drift, and `apply` after it merges (see the recipe below).

```yaml
toggles:
  checkout-v2:
    type: boolean
    default: false
    owner: team-payments
    expires: 2027-03-01
    environments:
      production:
        on: true
        off_value: false
        rules:
          - id: staff
            clauses: [{attribute: email, op: suffix, values: ["@example.com"]}]
            serve: {value: true}
        fallthrough:
          rollout: {buckets: [{value: true, weight: 500}, {value: false, weight: 9500}]}   # 5%
segments:
  beta-testers:
    rules: [{clauses: [{attribute: plan, op: equals, values: [beta]}]}]
```

### Changing a toggle as part of a delivery

There is no pipeline keyword for it yet. A step that runs the CLI does it, with an API token of the pipeline's
own (limited to the project and the operator role) from a secret store, and the recorded way "pipeline":

```yaml
  open-to-ten-percent:
    needs: [verify]
    image: your-registry/launchway-cli
    secrets: [LAUNCHWAY_TOKEN]
    run: launchway toggles set shop checkout-v2 production --percent 10 --reason "after verify of run $LAUNCHWAY_RUN"
```

The safeguards apply to it like to anyone: in a protected environment it waits for an approval.

## Where it is, and who may do what

| | Who |
|---|---|
| See toggles, history, usage | a viewer of the project |
| Make, change, delete a toggle; change its state; revert; approve | an operator of the project (in the environment, for a state change); a shared toggle needs the level's administrator |
| Safeguards, SDK keys | an administrator of the project (of the level, above it) |

Every action is in the [audit trail](../reference/events.md) (`toggle.*`); the toggle's own history keeps before
and after. The same operations are in the [HTTP API](../reference/http-api.md), the CLI (`launchway toggles`),
the MCP tools (`list_toggles`, `set_toggle_state`, and others; none makes or shows an SDK key) and the web pages
under a project's **Toggles** tab. A Terraform resource is planned (see [interface parity](../reference/interface-parity.md)).

## Not built yet

- A pipeline keyword, and a plan with steps and proofs between them ("5% at ten, 25% at eleven if the proofs pass").
- Turning a toggle back by itself when metrics worsen after a change (the *watch* marks the environment, it does not act).
- Reading the toggles file from your repository and opening the pull request that removes a finished toggle.
- Webhook events for toggle changes (webhooks are tied to repositories today); the audit trail and the history have them.
- Toggles that are not OpenFeature: OpenFeature providers exist for Go and TypeScript; Java, Python and .NET are coming.
- A streaming protocol other than this one (for example flagd's), and a relay in a zone.
