# Hardening checklist

What to do before anyone but you can reach a Launchway server, and good habits for pipeline
authors. For operators and platform engineers, and for developers who write pipelines.

## For operators

### Before others can reach the server

- [ ] **Turn on sign-in** (`auth.mode: oidc`) and grant roles with `auth.access`. Without sign-in,
  anyone who can reach the server can start pipelines, which run code on its Stations (runners), and approve
  deployments. A server without sign-in refuses to listen on anything but a loopback address
  unless `auth.allow_anonymous: true` says something else restricts who can reach it; use that
  only if that is really so. See [Sign-in with OIDC and roles](../howto/oidc-and-roles.md).
- [ ] **Give the fewest people `admin`.** Grant `viewer` to everyone who needs to see, `operator`
  to the teams that own projects (`projects:` patterns), and `admin` to a small group. Limit
  operators of production with `environments:` on their bindings.
- [ ] **Serve over HTTPS**, normally behind a reverse proxy. Set `server.trust_proxy: true` only if
  clients cannot reach Launchway except through that proxy. The proxy must not buffer
  `/api/v1/stream`.
- [ ] **Keep the client secret out of the file:** `client_secret_env` or `client_secret_file`.
- [ ] **Use PostgreSQL** with TLS and a user that owns only Launchway's database, for anything
  beyond one machine. **Protect the database and its backups**: it holds runs, logs and the audit
  trail. Anyone who can write to it can change who has done what.

### Encryption at rest

See [Encryption at rest](../guide/encryption-at-rest.md).

- [ ] **Have a root key, held outside the database**: `encryption.key_file` (or the chart's generated or
  existing Secret), a passphrase keystore, or Vault or OpenBao transit. `launchway doctor encryption`
  must say "a database dump alone cannot reveal secrets: yes". Set `encryption.require: true` (the chart
  does) so a server never starts without one.
- [ ] **Keep the key file private** (`0600`; a mounted Secret `0440`) and off the volume that holds the
  database. In Kubernetes, turn on etcd encryption at rest and limit who may read Secrets: the key is only
  as strong as that.
- [ ] **Back the root key up separately from the database's backups**, held by more than one person. A
  backup without the key cannot be read, and the key cannot be recovered.
- [ ] **Practice a restore once**: `launchway doctor encryption --restore-drill` against a restored copy.
- [ ] **Rotate** (`launchway encryption rotate`) at least yearly and when someone who held the key leaves;
  **replace the root key** (`rewrap`) after any exposure of it.
- [ ] **Encrypt the database volume and the Cargo (artifact) store as well.** Cargo, logs, build caches and Git
  packs are not yet encrypted by Launchway.
- [ ] **Watch the Logbook** for `encryption.unseal` with outcome `failed`.
### With the Helm chart

The chart ships these on by default; check that you have not switched one off.

- [ ] **Sign-in is required.** The chart's default is built-in accounts (`auth.mode: local`); the first
  administrator is made with a one-time setup code in a Secret. Do not set `auth.allow_anonymous` in a
  cluster others can reach.
- [ ] **Network policies are on** (`networkPolicy.enabled`): default deny for servers, Stations and step pods.
  Step pods can reach only DNS, the Launchway server and the internet over HTTP and HTTPS, never the
  database, the Kubernetes API or the cloud metadata service. Add your own egress with `extraEgress`;
  add your cluster API's address to `networkPolicy.steps.exceptCIDRs` if it is public.
- [ ] **Encryption at rest is on** and the **root key is kept**: it is not deleted by `helm uninstall`.
  Copy it outside the cluster and the database backups.
- [ ] **No secret material in the rendered chart.** The install hook Job makes secrets in the cluster; a
  GitOps repository or a rendered manifest never holds one.
- [ ] **Pods are restricted** (non-root, read-only root filesystem, no capabilities, seccomp) and have
  ephemeral-storage limits.

### Stations {#runners}

- [ ] **Enrol Stations with join tokens** instead of the shared Station token, give each pool only the
  projects it serves, and set `require_identity: true` on pools whose Stations are all enrolled. A
  Station on the shared token has no identity; see the
  [trust model](trust-model.md#current-limits-and-what-is-changing).
- [ ] **Keep the Station token and join tokens secret.** The server stores the shared Station token sealed (see Encryption at rest above); the data directory's `runner-token` file holds it too, mode `0600`. A token lets a machine take work, and with it
  pipelines and the secret references they hold. Make join tokens short-lived and ephemeral, and
  revoke Stations you no longer trust.
- [ ] **Give each Station an `accept:` section** limiting the projects, repositories, images and
  executors it takes, and refusing pull requests and drafts where they are not needed. The server
  cannot change it. See [Stations on other machines](../guide/runner-identity.md).
- [ ] **Connect Stations only to servers you run**, over HTTPS.
- [ ] **Set `executor:` explicitly** on shared Stations. `auto` falls back to running steps on the
  host when no container runtime is found.
- [ ] **Keep step pods restricted** (`restricted: true`, the default) and label the step namespace
  `pod-security.kubernetes.io/enforce=restricted`.
- [ ] **Give each secret store allow-lists** of `projects` and `keys`. An empty list allows every
  project to read every key the Station can read.
- [ ] **Do not define an `env` secret store on a shared Station** unless you mean pipelines to read
  the Station's Airfield (environment).
- [ ] **Give Git credentials through `GIT_CONFIG_*` settings**, not inside repository addresses,
  and give them only the rights they need. Protect important branches on the Git host.
- [ ] Behind a TLS-inspecting proxy, give the Station the proxy's certificate authority with
  `SSL_CERT_FILE`; `HTTPS_PROXY` is honoured.

### Images and deployments

- [ ] **Scope Depot (registry) accounts**: one robot account per project or team, limited to its
  repositories. Name the projects in `pull_projects` and give push rules per project.
- [ ] **Run BuildKit rootless in its own namespace**, as `deploy/buildkit` does, and treat each
  daemon as a trust boundary.
- [ ] **Keep the Station's signing key on the Station only** (`images.signing`), not in a secret store
  pipelines can read.
- [ ] **Run deploy Stations with the chart's least-privilege RBAC** and name the rollouts they may
  control in `delivery.deployRunner.rbac.controlRollouts`.
- [ ] **Mark production Airfields `protected: true`.**
- [ ] **Enforce policies for production**: tests, findings, SBOM, signed images built by the Sortie (run),
  approvals by someone else, and pinned library versions. Start each in `report` mode, then
  `enforce`.

### People and records

- [ ] **Revoke the API tokens of people who leave.** Their tokens keep their last known groups
  until they expire. Keep `auth.tokens.max_days` short.
- [ ] **Keep the Logbook.** Launchway only adds to it; copy it to your log archive if you must
  keep it longer than the database.
- [ ] **Watch for failed sign-ins and refused tokens** (`token.use`, `webhook.*` with outcome
  `denied`). See [Monitor Launchway](../howto/monitoring.md).

## For pipeline authors

- [ ] **Never put secrets in `env`, inputs, outputs, build arguments or Cargo.** Use
  `secrets:`. Values in Cargo are not masked, and inputs are recorded and shown.
- [ ] **Pin images by digest** (`image@sha256:...`) where it matters, and pin libraries to tags or
  commits rather than branches.
- [ ] **Do not put untrusted containers in a pipeline that uses secrets.** On Kubernetes a step's
  Airfield is readable by the Sortie's other containers.
- [ ] **Keep deployment steps out of pull requests** with `when` conditions, and leave
  `pull_requests.secrets` off.
- [ ] **Use approvals and Return to base (rollback) steps** for production deployments.
- [ ] **Keep test and scan steps failing when they should**: capture the exit status before
  writing reports, as in the [Debriefs (test reports) guide](../guide/test-reports.md).

## What next

- [Trust model](trust-model.md).
- [Supply chain](supply-chain.md).

## Sign-in (built-in accounts)

- A server that listens beyond its own machine never starts without sign-in: it uses built-in accounts unless you
  chose an identity provider. `auth.mode: none` is refused anywhere but loopback.
- Keep a second factor required for administrators (`auth.local.require_second_factor: admins`, the default).
- Keep the first administrator's recovery codes and a copy of the root key somewhere separate. Run
  `launchway users list` now and then and remove accounts nobody uses.
- With an identity provider, leave one built-in administrator as the break-glass account; remove it only after a
  provider sign-in has worked. See [Built-in accounts](../guide/built-in-accounts.md).
