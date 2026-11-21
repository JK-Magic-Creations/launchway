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
  `encryption.keyFile.existingSecret`, the Job makes none. The Station (runner) token, the bundled database's password and
  the setup code follow the same rule; ones you gave (`runnerToken.existingSecret`, `postgresql.password`,
  `database.existingSecret`) are never touched.
- **Existing secrets are sealed in place, after the rollout.** The new servers start with the key and keep
  reading what is in the clear. They seal it once no server of the previous release has been seen for 45
  seconds, so an old server never reads a sealed value and there is **no window** in which sign-in, webhooks or
  Git run tokens fail during a rolling update, a Pathfinder (canary) or an Argo Rollout, however long it takes. Watch it with
  `launchway doctor encryption` ("waiting for older servers", then "complete"). New secrets made meanwhile are
  sealed at once. Set `LAUNCHWAY_SEAL_QUIET_SECONDS=0` (server Airfield (environment)) to seal at start instead, which brings
  back the window: a few seconds in the rehearsal, during which an old server fails to read webhook addresses.
- **Schema.** Migrations 34 (encryption), 35 (build caches), 36 (built-in accounts) and 47 (Switches (feature toggles)) are
  additive; the previous build runs against the new schema. Numbers 37 to 46 are held, empty, for work that has not
  merged, so that 47 is the number it will have when they do.

## Sign-in on upgrade

An installation keeps the sign-in it was configured with.

- `auth.mode: oidc` is untouched. API tokens, sessions and the Station token keep working.
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
   **before** upgrading (allowed by the previous chart). The chart refuses sizes under 2Gi when Cargo (artifacts) are kept in
   the database: set `config.artifacts.store` or enlarge the claim first. Installations with their own database
   are not affected.
3. **`externalURL`** is required only for a new installation with built-in accounts, or with `ingress` or
   `httpRoute`; an installation with its own sign-in that publishes nothing through the chart needs none.

## Returning to base {#rolling-back}

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
