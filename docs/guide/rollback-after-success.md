# Return to base (rollback) after success {#rollback-after-success}

Put a service back to an earlier version after its deployment succeeded, when you find a problem later.

A pipeline's Return to base step only undoes a deployment that fails its own checks. This page is about the
other case: the deployment passed, the Sortie (run) is finished, and then customers report that something is
wrong. You can roll back from the application page, the Airfield (environment) page or the deployment's panel on its
run, from the command line (`launchway deployments rollback`) or through an agent (`rollback_deployment`).
The button is only shown where it will work; otherwise it is shown unavailable with the reason.

## When to use it

Use it when the version that runs now is bad and an earlier successful version is good. It is for
emergencies as much as for ordinary changes of mind: you need the operator role on the Flight (and on the
Airfield), a reason, and nothing else. It is not a way to deploy a new version. To go forward, run the
pipeline.

## What it does

A Return to base is a **run of its own**, in the same Flight, that you ask for and that has one step. The Sortie
that made the deployment is never changed; it only gains a link, "returned to base later by run #N", and the
Return to base run links back to the versions it restored.

1. The Return to base takes the Airfield's **lock**, like any deployment.
2. A deploy Station (runner) serving the Airfield's target **reverts in Git**, newest first, the commit of every
   deployment after the version you chose, one revert commit each, and pushes them together. Reverts are
   all or nothing.
3. Argo CD syncs, and Launchway watches until the application (and its rollout) is healthy.
4. Launchway records a **new deployment** of the restored version, whose previous version is the one that
   was running. It appears on the launch line, in Activity, on the Airfield's history ("returned to base
   from v1.4.0 to v1.3.2 by Ann: the reason") and in the Logbook.

### Why it reverts commits, and when it refuses

Git stays the truth. To roll back to version V, Launchway reverts every deployment commit after V that is
still in effect for that service, newest first. The other way to do it, writing V's values over the files,
would also erase any change that was not a deployment, such as a hand edit or a scaling change, and
would leave no record of what was undone. A revert keeps history and shows exactly what was undone.

If a revert does not apply, because later commits changed the same lines, nothing is changed in Git and the
Return to base run fails with a plain sentence naming the deployment, the files and what to do: fix those files
in the GitOps repository by hand, or deploy the version you want with a normal run.

### When the old chart cannot render the values

A deployment that changed both the chart pin and the values is reverted in two commits, values first, then the
pin. Before the pin revert is committed, the Station that serves the Airfield reads the old chart's `values.schema.json` at the old
pin (when the chart is in a Git repository the Station is configured for) and compares it with the values the
application will read. If the old chart does not accept them (for example `additional properties
'networkPolicy' not allowed`), the pin is **not** reverted. The step fails with one plain message: which chart pin
and which keys, that Git is at the new pin (what actually runs: the new chart with the previous image), and the
two next actions: remove or rename those keys and Return to base again, or Launch a fixed version forward. The
Airfield (environment) is marked as needing a person. A chart served from a chart repository cannot be read by the Station;
the check is then skipped and said so in the step's log.

If the pin revert is committed and the controller still cannot render the application (a ComparisonError that
lasts), Launchway reverts its own pin revert, so Git again names what runs, and reports the same way. If it
cannot, the message names the exact commit to revert. A deployment that is not in progress can still be left
with a controller operation retrying stale revisions; the Station has a check for it (a command to run it by hand is not built yet) that finds it and, with
the clear option, ends it. It needs `get` and `patch` on `applications.argoproj.io` in the Argo CD namespace,
which the Station already has.

"Still in effect" means a successful deployment that no Return to base has undone. A deployment that failed, one
that its own pipeline's Return to base step reverted, and one an earlier Return to base undid are never reverted
again, and are not offered as targets.

## Choosing the target

By default the service goes back to the **previous successful deployment**. **Roll back to…** lists every
earlier successful deployment still in effect: its version, when it was deployed, which Sortie made it, and what
choosing it would revert. Choosing an older one reverts more.

## The confirmation

Before anything happens you are told what will happen, for example: "dev goes from v1.4.0 back to v1.3.2;
the 3 deployments after it (runs #18, #17, #16) are reverted in Git, newest first; the database and
configuration are not returned to base." Suggestions follow, such as checking that a database migration in the
version being undone is backward compatible. They do not stop you. You must give a **reason**, which is
recorded with the Sortie, and then it goes ahead.

## What it does not do

A Return to base restores the version only. It never restores a database or configuration, and says so in the
confirmation and in the result. If the version you are undoing changed a schema, check that the
older version still works with it first.

It does not restore **Switches (feature toggles)** by itself either, but it tells you about them. The confirmation lists the
[toggle changes](feature-toggles.md) made in the Airfield (environment) since the version it restores went live, and offers to
revert them with the Return to base (`--revert-toggles` on the command line). It is unchecked by default: it is your call
whether they belong to what you are undoing. They are reverted after the Return to base is accepted, never held for an
approval, and each one is recorded. Restoring a version together with its configuration as one "stable state" record
is future work.

## When the action is not offered

The server's `actions` list says why, and what would help:

| Reason | What would help |
|---|---|
| No earlier successful deployment | Deploy a version first |
| No deploy Station serves the Airfield's target | Start or repair the deploy Station |
| The deploy Station cannot write the GitOps repository | Give it write access (its self-check reports this) |
| The last Return to base failed, so what Git holds is not known | Look at that Sortie; fix the repository by hand or deploy normally |
| Another deployment holds the lock | **Queue the Return to base behind it**, or cancel that Sortie first |
| You lack the operator role | Ask an administrator |

An action reported available succeeds, and one reported unavailable is refused with the same sentence. A
Return to base queued behind a running deployment starts when the lock is free, ahead of other waiting
deployments. It then checks that the service still runs what it did when you asked; if another deployment
changed that meanwhile it fails without changing anything, and you ask again.

## Protected Airfields and review {#protected-environments-and-review}

On a [protected Airfield](deployments.md) a Return to base is an **emergency action**: no approval comes first,
only the reason. The Sortie is flagged **review required** and appears under **Needs review** for the
Airfield's approvers until one acknowledges it with a comment, which is recorded. The person who asked for
the Return to base may not acknowledge it (unless `approvals.allow_self` is on), and with `reviewers` set on the
Airfield only those people may.

An installation or an Airfield can ask for approval even in an emergency:

```yaml
approvals:
  emergency_rollback: allow          # the default; or require_approval
environments:
  - name: prod
    protected: true
    emergency_rollback: require_approval   # overrides the installation's setting
    reviewers: { groups: [sre] }           # who may acknowledge an emergency rollback
```

With `require_approval` the Return to base run has an approval step first, which the person who asked for it cannot
give, and it needs no review afterwards.

## From the command line and agents

```text
launchway deployments options  --env dev
launchway deployments rollback --env dev --to 1.3.2 --reason "checkout fails since 1.4.0"
launchway deployments review list
launchway deployments review RUN_ID --comment "checked the logs; the revert matches the incident"
```

The MCP tools are `get_rollback_options`, `rollback_deployment`, `list_rollback_reviews` and
`review_rollback`. The routes are in the [HTTP API](../reference/http-api.md), the events and audit actions
(`deployment.rollback`, `rollback.review`, `run.reviewed`) in [events](../reference/events.md).

## Next

- [Deployments](deployments.md), the deploy step and rollout control
- [Approvals and re-runs](approvals-and-reruns.md)
- [Airfields and deployments](../learn/concepts/environments-and-deployments.md)
