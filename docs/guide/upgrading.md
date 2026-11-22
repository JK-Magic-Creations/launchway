# Upgrading Launchway

An upgrade is **change the version, nothing else**. That holds for `helm upgrade`, for a GitOps controller
(Argo CD or Flux, one application or an app-of-apps layout), for Launchway deploying itself, and for
re-running `launchway install`; and for the image and the chart arriving together or one after the other.
A rollback works too. This page says how that is arranged, what Launchway checks for you, and what
you will see when something is not as expected.

The release gate that proves it is in [Releasing](../development/releasing.md): `make test-upgrade`
upgrades the previous release to this one by each method on a throwaway cluster, rolls back, and fails the
release if anything needed a manual step.

> **Verified** by that gate, from the previous release (commit `093664b`, image `0.1.0`) to this one
> (`0.2.0`): `helm upgrade`, a simulated GitOps controller (single application, app-of-apps where the
> child syncs before the parent, image first, chart first) and rollback before and after sealing. The
> GitOps controller in those cells is a script that renders with `helm template` and applies in the
> order Argo CD does (hooks first, then the rest, then prune); a real Argo CD was exercised by
> the deploy step's own tests, not by this matrix. The network plugin was kind's default, which
> enforces policy.

## What an upgrade does

- **Nothing is generated while rendering.** `helm template` is the same every time and holds no secret,
  so a controller that renders on every sync never replaces a key.
- **The root key is made once**, by the chart's install hook Job (`launchway bootstrap-secrets`, a
  `pre-install` and `pre-upgrade` hook, which Argo CD runs as PreSync): the `<release>-launchway-root-key`
  Secret if it is absent. A second sync says "already present" and changes nothing. The runner token, the
  bundled database's password and the setup code follow the same rule. Ones you gave are never touched.
  **Copy the root key out of the cluster** and keep it with your other secrets; nothing recovers it.
- **The previous chart's Secrets are kept.** The previous chart rendered the database password and the
  runner token itself, so a release owned them and would have deleted them when the new chart stopped
  rendering them. The hook Job marks the existing ones `helm.sh/resource-policy: keep` and
  `argocd.argoproj.io/sync-options: Prune=false` (annotations only; the data is never touched).
- **Existing secrets are sealed after the rollout.** The new servers start with the key and keep reading what
  is in the clear. They seal it once no server of the previous release has been seen for 45 seconds, so an
  old server never reads a sealed value and a rolling update, canary or Argo Rollout has **no window** of
  failing sign-in or webhooks, however long it takes. Watch it with `launchway doctor encryption`
  ("waiting for older servers", then "complete").
- **The schema is additive.** The previous build runs against the new schema.
- **The bundled database's volume does not change.** The default is 1Gi, what the previous chart made: a
  claim cannot shrink, and most StorageClasses cannot grow one. Set `postgresql.persistence.size` for a
  larger volume on a **new** installation.

## The compatibility rule

The image and the chart are one release, but each may arrive first.

| Pair | What happens |
|---|---|
| this chart, this image | everything works |
| this chart, the previous image | the chart notices from `image.tag` (or `image.bootstrap`) that the image predates the secrets bootstrap, and renders **no hook Job, no key mount and no encryption environment**: the previous image runs as it always did |
| the previous chart, this image | the server starts **not encrypted at rest**, creates no key (it would not know where to keep it) and says so |
| a tag outside the chart's range | the render fails, with a sentence |

It is encoded in three places. The chart declares `launchway.io/supported-app-versions` (now
`>=0.1.0 <0.3.0`) and `launchway.io/bootstrap-since`; a tag outside the first fails the render with
"This chart (0.2.0) supports Launchway images >=0.1.0 <0.3.0, and image.tag is 0.0.5"; an image older
than the second gets the compatibility render above. The server declares the chart revision it expects
(`launchway.io/chart-revision`, now 2) and logs it at start: "rendered by chart revision 2, as this server
expects", or a warning when the chart is older: "this server was rendered by an older chart: encryption and
network settings from the new chart are not applied yet". Every release keeps the rule: **a chart works with
the image of the release before it, and an image works with the chart of the release before it.** A release
that cannot say so is not cut.

If you pin the image by digest only, also set `image.tag` to the version (or `image.bootstrap`), because
the chart reads the version from the tag.

The image has no shell, so the hook Job cannot probe the binary; the chart decides from the tag instead.

## Not encrypted yet

An installation upgraded under a chart that does not mount the key (the previous chart with the new image) runs
**not encrypted at rest** until the chart that belongs to the image is deployed. There is one path:

> deploy the chart that belongs to this image (its install hook makes the root key and mounts it), then
> restart the servers. Nothing else needs setting.

The server says it at every start ("YOUR INSTALLATION IS NOT ENCRYPTED AT REST YET: deploy the chart that
belongs to this image ...") and `launchway doctor` repeats it. Turning encryption on later is safe with
several servers: they all start with the key after the restart, the first to start seals, and an
older server still running keeps the sealing waiting.

## Values from a newer chart

The chart's values schema is open on purpose. A key this chart version does not know does not stop the render:
it is ignored, listed in the install notes, in the Deployment annotation `launchway.io/ignored-values`, in the
server's start-up warning and in `launchway doctor`:

> 3 values were ignored by this chart version: networkPolicy.fromTheFuture, someSetting, typo: a typo, or a
> setting for a newer chart.

That lets you prepare a value ahead of an upgrade and roll the chart back without editing the values. Known
keys keep their types: `server.replicas: many` still fails.

**strictValues.** `strictValues: true` makes unknown keys fail the render. Use it in `helm lint`, in the CI of your
own values and while typing them (`make chart-lint` and the installer's own render do); leave it `false` in what
a GitOps controller renders, so a rollback still renders. Releases **before this one** have a closed schema:
rolling back to one of them needs the keys that only a newer chart knows removed first.

## Network policies

The chart's policies are never wider than "nothing else is there": the server admits its own namespace, the
Gateway's namespace and `networkPolicy.server.proxyNamespaces` (never any source), and step pods get DNS and the
server only; the internet is an opt-in (`networkPolicy.steps.internet: true`).

On a cluster whose network plugin has its own policy objects that **add** to Kubernetes ones (CiliumNetworkPolicy,
Calico's GlobalNetworkPolicy), creating the chart's as well can widen access and objects stay behind when they
are turned off. Let one system be the authority. `launchway install` and `launchway doctor network` find a
policy already in the namespace (Kubernetes NetworkPolicies that are not Launchway's own, and any
`*NetworkPolicy` kind from a non-core API group), say so, and leave the chart's off by default
(`networkPolicy.enabled: false`), asking when interactive.

**Upgrading onto a cluster that enforces policy and had none** turns the chart's policies on. If something in
the cluster is reached by the servers (an identity provider, an in-cluster Git server), list it in
`networkPolicy.server.extraEgress` and `networkPolicy.runner.extraEgress` (the gate does this for its test Git
server), and builds that download dependencies need `networkPolicy.steps.internet: true`.

With pruning off in your controller, objects the new render no longer includes (a NetworkPolicy after
`networkPolicy.enabled` became false) stay in the cluster. The deploy step names them ("2 objects from an
earlier render are still present and are no longer part of the release") and **fails** when any is a
security-relevant kind (NetworkPolicy, Role, RoleBinding, ClusterRole, ClusterRoleBinding, ServiceAccount, Ingress and
route kinds, Secret) unless the step sets `sync.leftovers: warn`. Nothing is deleted for you.

## Upgrading with a GitOps controller

Change the image tag (and digest) and the chart pin; that is all. When one deployment changes an Argo CD
application's **own manifest** (its chart pin, applied by a parent application) and its **values**, the deploy
runner does not write one commit: it writes the manifest first, asks the parent to refresh, **waits until
the cluster shows the new pin in the application's spec**, and only then writes the values. The step log says
which mode is in use: *CONTROLLER-ORDERED*, or *FALLBACK, two commits with a pause*. If the parent never applies the
pin, the step fails and **the values are not written**. A child that syncs before the parent, with new values
and the old chart, is harmless anyway: the previous chart refuses values only the new chart knows (nothing is
applied) until the parent applies the pin.

If you deploy by hand, change the chart pin first, wait until the parent has applied it, then the image. Never the
other way round in one commit. Roll back in the reverse order.

## Rolling back

**Before sealing finishes** (`launchway doctor encryption` has not said "complete"): put the previous image and chart
back, by any method. Nothing was changed that the previous release cannot read. No manual step.

**After sealing**: the previous release cannot read sealed values (sign-in state, webhook secrets, SSH host keys). It
refuses to start and says why (the gate saw "the SSH host keys: the ed25519 host key: cannot be opened with this
server's key (was the sealing key changed?): not a sealed value"; an earlier build that did start failed sign-in,
webhook deliveries and Git run tokens instead). Nothing is corrupted: it only reads. To go back for real, restore the dump taken before the
upgrade, which the gate tests end to end:

```sh
# 1. put the previous image and chart (and values) back, by your method; then stop the servers
kubectl -n launchway scale deploy/<release>-launchway-server --replicas=0
# 2. empty the schema and load the dump taken BEFORE the upgrade
kubectl -n launchway exec deploy/<release>-launchway-postgresql -- psql -U launchway launchway -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
kubectl -n launchway exec -i deploy/<release>-launchway-postgresql -- psql -q -U launchway launchway < before-upgrade.sql
# 3. start the servers
kubectl -n launchway scale deploy/<release>-launchway-server --replicas=2
```

Take the dump before every upgrade (`pg_dump`) and keep it until sealing has finished and you trust the new
release. Anything made after the dump is lost. **Do not delete the root key Secret**: a database sealed with it
cannot be read without it.

## What you will see

| Message | Meaning and what to do |
|---|---|
| `This chart (0.2.0) supports Launchway images >=0.1.0 <0.3.0, and image.tag is X` | the image is outside the chart's range: deploy the chart that belongs to it |
| `strictValues is true and this chart (...) does not know these values: ...` | a typo, or a value for a newer chart: fix the name, or set `strictValues: false` |
| `N values were ignored by this chart version: ...` | the same keys, with strictValues off: nothing here uses them |
| `YOUR INSTALLATION IS NOT ENCRYPTED AT REST YET: deploy the chart that belongs to this image ...` | the new image is running under the previous chart: deploy the chart, then restart the servers |
| `this server was rendered by an older chart: encryption and network settings from the new chart are not applied yet` | the same cause |
| `stored secrets are still in the clear and servers of an older release may be running: sealing them waits` | normal during a rollout; sealing starts 45 seconds after the last old server stops |
| `cannot be opened with this server's key (was the sealing key changed?): not a sealed value` (in the previous release's log) | you rolled back after sealing: restore the dump, above |
| `this cluster already has network policy in place: ...` | let one system be the authority; the installer left the chart's off |
| `Network policy: WARNING ...` from `launchway doctor network` | the same, for an installation that exists |
| `the Secret X exists but has no "root.key" key` | a Secret you made under the name the chart uses has no such key: add it, or delete the Secret so a new one is made |
| `ComparisonError` in Argo CD naming a values key | the chart pin and the values are not in step: the parent has not applied the pin yet |

## If a sync is stuck

While a deployment waits for the controller, the step's log carries the controller's own account (sync and
health, the operation's phase and message and retry count, the failing hook or resource). A `ComparisonError`
that lasts (default 20 seconds) fails the step with its full text. If the controller is retrying a sync that
was started with revisions the application no longer points at, the step says so and ends that operation (it
removes the application's `operation`; this needs the right to patch that application) so the application's own
sync starts a fresh one.
