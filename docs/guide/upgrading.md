# Upgrading from the release before encryption at rest and built-in accounts

For anyone who runs Launchway with the Helm chart, including under Argo CD or Flux, and has an installation
from the release before this one. A fresh installation does not need this page: see
[Install in ten minutes](../howto/install-in-ten-minutes.md).

> **Verified** on a throwaway kind cluster (Kubernetes with the chart rendered only by `helm template` and
> applied the way Argo CD does it: hook Jobs first as PreSync, then the rest, no `lookup`), from the chart and
> image of the commit before to this release, with the values file unchanged, twice from scratch.

## What an upgrade does

- **One command renders it, nothing special is needed.** The chart is deterministic: `helm template` is the
  same every time and holds no secret.
- **The root key is made once.** The chart's install hook Job (`launchway bootstrap-secrets`; a
  `pre-install`/`pre-upgrade` hook, which Argo CD runs as PreSync) creates the `<release>-launchway-root-key`
  Secret if it is absent. A second sync says "already present" and changes nothing. If you name your own Secret in
  `encryption.keyFile.existingSecret`, the Job makes none. The runner token, the bundled database's password and
  the setup code follow the same rule; ones you gave (`runnerToken.existingSecret`, `postgresql.password`,
  `database.existingSecret`) are never touched.
- **Existing secrets are sealed in place, after the rollout.** The new servers start with the key and keep
  reading what is in the clear. They seal it once no server of the previous release has been seen for 45
  seconds, so an old server never reads a sealed value and there is **no window** in which sign-in, webhooks or
  Git run tokens fail during a rolling update, a canary or an Argo Rollout, however long it takes. Watch it with
  `launchway doctor encryption` ("waiting for older servers", then "complete"). New secrets made meanwhile are
  sealed at once. Set `LAUNCHWAY_SEAL_QUIET_SECONDS=0` (server environment) to seal at start instead, which brings
  back the window: a few seconds in the rehearsal, during which an old server fails to read webhook addresses.
- **Schema.** Migrations 34 (encryption), 35 (build caches) and 36 (built-in accounts) are additive; the previous
  build runs against the new schema.

## Sign-in on upgrade

An installation keeps the sign-in it was configured with.

- `auth.mode: oidc` is untouched. API tokens, sessions and the runner token keep working.
- `auth.mode: none` with `allow_anonymous: true` (the explicit opt-in the previous release already required on an
  address others can reach) keeps running exactly as before. It is **not** switched to built-in accounts, nobody is
  locked out, and it does not stay open silently: every page shows "This installation has no sign-in", the server
  logs it at every start, and `launchway doctor` reports it. **Set up sign-in now** on that page gives a one-time
  setup code (shown once) and records the request; when the servers next restart they run with built-in accounts
  and ask for the code on the first page. Nothing changes until then. You can also set `auth.mode: local` or `oidc`.
- A configuration that never wrote `auth.mode` is `local` when it listens beyond loopback (it never starts open)
  and `none` on loopback.

## Things in the new chart that can need a values change

The previous chart's values schema rejects keys it does not know, so a new key can only go in **after** the
chart pin moves. Move the image and the chart pin in one commit with the values unchanged; add values in the
next commit, only if you need them:

1. **Network policies are on by default** (default deny). Where the cluster enforces them, the servers can then
   reach only DNS and the database: an identity provider, a Git host or a webhook receiver must be listed in
   `networkPolicy.server.extraEgress`, and a proxy in `networkPolicy.server.ingressFrom`; or set
   `networkPolicy.enabled: false`. In the rehearsal the first sync broke OpenID sign-in and webhook deliveries until
   `extraEgress` was added. Where your own policies (for example Cilium) already allow what the servers need, the
   chart's policies only add to them and nothing breaks.
2. **The bundled database's volume** is 8Gi by default (the previous chart's was 1Gi). A claim cannot shrink, and
   one cannot grow without volume expansion, so set `postgresql.persistence.size` to the size of your existing claim
   **before** upgrading (allowed by the previous chart). The chart refuses sizes under 2Gi when artifacts are kept in
   the database: set `config.artifacts.store` or enlarge the claim first. Installations with their own database
   are not affected.
3. **`externalURL`** is required only for a new installation with built-in accounts, or with `ingress` or
   `httpRoute`; an installation with its own sign-in that publishes nothing through the chart needs none.

## Image and chart must move together

The server image and the Helm chart are one release: the chart gives the server what the new image relies on
(the mounted root key, environment variables, network policies). A pod with the **new image rendered by the
old chart** has none of that. For example, it has no root key and no `LAUNCHWAY_ENCRYPTION_REQUIRE`, and the
next pods, rendered by the new chart, refuse to start.

**What Launchway does about it.**

- *The server never invents a key where it could be lost.* With no root key configured, a server on PostgreSQL, or
  on a database that already holds data, creates no key and runs not encrypted, with a warning (see
  [Encryption at rest](encryption-at-rest.md)). An image-only upgrade under an old chart therefore keeps
  working, and `launchway encryption recover` repairs an installation that already created a throwaway key.
- *The server says which chart rendered it.* At start it logs the chart revision (`LAUNCHWAY_CHART_REVISION`,
  set by the chart) against the revision it expects, and warns when the chart is older or does not say:
  "this server was rendered by an older chart: encryption and network settings from the new chart are not
  applied yet".
- *The deploy step orders the two changes.* When one deployment changes an Argo CD application's **own manifest**
  (its chart pin, `targetRevision`, applied by a parent application in an app-of-apps layout) and its **values**
  (the image tag and digest), the deploy runner no longer writes one commit. It writes the manifest first,
  asks the parent application to refresh (and to sync, when it does not sync by itself and the runner may sync
  it), **waits until the cluster shows the new pin in the application's spec**, and only then writes the values.
  After the sync it checks that every source was synced at what the deployment intended; a sync that ran with a
  mixed set is reported plainly and followed by a corrective sync. The step log says which mode is in use:
  *CONTROLLER-ORDERED*, or *FALLBACK, two commits with a pause* where the cluster cannot be read (the runner then
  waits a fixed time instead of watching the pin arrive). If the parent never applies the pin, the step fails
  and **the values are not written**, so nothing mixed can happen; run it again after syncing the parent.
  An ApplicationSet is refused with a plain message. An application without a parent and a deployment that
  changes only values or only the manifest keep the old behavior: one commit.
- What the runner needs: get and patch on the application (refresh, sync) and get on the parent; syncing a parent
  also needs the parent to be listed in `deploy.targets.<name>.applications` for the project. Without the right
  to patch, the runner waits for the parent to apply the pin itself.
- Alternatives considered: pinning the values to a commit in the application (no race, but every release then
  edits the application manifest too); disabling automated sync around a release (fights the parent's self-heal);
  one commit (the race this section is about).

**Rollback, stuck operations and what the step says while it waits.** A rollback of a deployment that was
written as two commits reverts the values first (the previous image under the new chart) and then the application's
own manifest in a commit of its own, and waits for the cluster to show the old pin. While a deployment waits for
the controller, the step's log carries the controller's own account (sync and health, the operation's phase and
message and retry count, the failing hook or resource with its message) each time it changes. A `ComparisonError`
that lasts (default 20 seconds) fails the step with its full text. If the controller is retrying a sync that
was started with revisions the application no longer points at, the step says so ("started with source 0 at X; the
application now points at Y") and, unless the runner is configured with `KeepStaleOperations`, ends that operation
(it removes the application's `operation`, which needs the right to patch that application, the same right used for
refresh and sync, and is done only for the application being deployed) so the application's own sync starts a
fresh one. Not done yet: checking before a rollback that the chart being rolled back to still accepts the current
values (a values key only the new chart knows makes the old chart refuse to render; the ComparisonError above now
names the key, but the rollback does not yet remove it).

**What "compatible" means.** Between the two commits the application may sync the **new chart with the old
image**. A chart must therefore work with the image of the release before it (the rule for every chart change:
additive values, no required new mount). Where a release cannot do that (the chart removes something the old image
needs), neither order is safe: schedule it with the application's automated sync paused, or deploy with
a window in which the application is not serving.

**If you deploy by hand** (no deploy step): change the chart pin (the Application manifest) first, wait until the
parent has applied it (the application's spec shows the new `targetRevision`), and only then change the image tag
or digest. Never in one commit and never the other way round. Roll back in the reverse order. A rollback by Launchway
reverts the values commit only; the chart pin stays at the new revision, which is safe for the same
compatibility reason.

**Leftovers.** With pruning off, objects the new render no longer includes (a NetworkPolicy after
`networkPolicy.enabled` became false) stay in the cluster. The deploy step names them ("2 objects from an earlier
render are still present and are no longer part of the release"). The chart's own hook objects (the bootstrap Job's
service account, role and binding, annotated `helm.sh/hook`) are not leftovers, and neither are objects owned by
something the release renders. A real leftover **never fails the step and never rolls the deployment back**: the new
version is running and healthy, so the deployment ends "delivered, with a problem to fix" (phase `leftovers`) when
a leftover is a security-relevant kind (NetworkPolicy, Role, RoleBinding, ClusterRole, ClusterRoleBinding,
ServiceAccount, Ingress and route kinds, Secret), unless the step sets `sync.leftovers: warn`; other kinds only
warn. Nothing is deleted for you: remove them with `kubectl delete`, or turn pruning on.

## Rolling back

Going back to the previous image and chart is clean **until the sealing has finished**: nothing was changed that
the previous release cannot read, and the schema is additive. After `launchway doctor encryption` says "complete",
the previous release cannot read sealed values (sign-in state, webhook addresses and secrets, SSH host keys):
it starts, and its sign-in, webhook deliveries and Git run tokens fail.

1. Take a database dump **before** the upgrade (`pg_dump`), and keep it until sealing has finished and you trust
   the new release.
2. To go back after sealing: put the previous image **and** the previous chart pin back (revert the commit and any
   later values commit that uses new keys; the previous chart refuses them), restore the dump, and keep the
   root key Secret. A Git revert alone does not restore the data. Anything made after the dump is lost: this is the
   price of sealing, and the reason to wait for a quiet period before relying on it.
3. Do not delete the root key Secret: a database sealed with it cannot be read without it.

The chart refuses values it does not know, so an automatic or manual rollback that pins the previous chart while
the values file holds a new key (for example `networkPolicy`) cannot render. Launchway's own rollback checks the
old chart's `values.schema.json` before it reverts the pin; if the old chart refuses the values it does not revert
the pin, says which keys and why, and leaves Git at the new pin, matching what runs ("held: needs a person").
