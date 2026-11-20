# Hardening checklist

What to do before anyone but you can reach a Launchway server, and good habits for pipeline
authors. For operators and platform engineers, and for developers who write pipelines.

## For operators

### Before others can reach the server

- [ ] **Turn on sign-in** (`auth.mode: oidc`) and grant roles with `auth.access`. Without sign-in,
  anyone who can reach the server can start pipelines, which run code on its runners, and approve
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

### Runners

- [ ] **Keep the runner token secret.** It lets a machine take work, and with it pipelines and the
  secret references they hold. Every build runner shares it today; see the
  [trust model](trust-model.md#current-limits-and-what-is-changing).
- [ ] **Connect runners only to servers you run**, over HTTPS.
- [ ] **Set `executor:` explicitly** on shared runners. `auto` falls back to running steps on the
  host when no container runtime is found.
- [ ] **Keep step pods restricted** (`restricted: true`, the default) and label the step namespace
  `pod-security.kubernetes.io/enforce=restricted`.
- [ ] **Give each secret store allow-lists** of `projects` and `keys`. An empty list allows every
  project to read every key the runner can read.
- [ ] **Do not define an `env` secret store on a shared runner** unless you mean pipelines to read
  the runner's environment.
- [ ] **Give Git credentials through `GIT_CONFIG_*` settings**, not inside repository addresses,
  and give them only the rights they need. Protect important branches on the Git host.
- [ ] Behind a TLS-inspecting proxy, give the runner the proxy's certificate authority with
  `SSL_CERT_FILE`; `HTTPS_PROXY` is honoured.

### Images and deployments

- [ ] **Scope registry accounts**: one robot account per project or team, limited to its
  repositories. Name the projects in `pull_projects` and give push rules per project.
- [ ] **Run BuildKit rootless in its own namespace**, as `deploy/buildkit` does, and treat each
  daemon as a trust boundary.
- [ ] **Keep the runner's signing key on the runner only** (`images.signing`), not in a secret store
  pipelines can read.
- [ ] **Run deploy runners with the chart's least-privilege RBAC** and name the rollouts they may
  control in `delivery.deployRunner.rbac.controlRollouts`.
- [ ] **Mark production environments `protected: true`.**
- [ ] **Enforce policies for production**: tests, findings, SBOM, signed images built by the run,
  approvals by someone else, and pinned library versions. Start each in `report` mode, then
  `enforce`.

### People and records

- [ ] **Revoke the API tokens of people who leave.** Their tokens keep their last known groups
  until they expire. Keep `auth.tokens.max_days` short.
- [ ] **Keep the audit trail.** Launchway only adds to it; copy it to your log archive if you must
  keep it longer than the database.
- [ ] **Watch for failed sign-ins and refused tokens** (`token.use`, `webhook.*` with outcome
  `denied`). See [Monitor Launchway](../howto/monitoring.md).

## For pipeline authors

- [ ] **Never put secrets in `env`, inputs, outputs, build arguments or artifacts.** Use
  `secrets:`. Values in artifacts are not masked, and inputs are recorded and shown.
- [ ] **Pin images by digest** (`image@sha256:...`) where it matters, and pin libraries to tags or
  commits rather than branches.
- [ ] **Do not put untrusted containers in a pipeline that uses secrets.** On Kubernetes a step's
  environment is readable by the run's other containers.
- [ ] **Keep deployment steps out of pull requests** with `when` conditions, and leave
  `pull_requests.secrets` off.
- [ ] **Use approvals and rollback steps** for production deployments.
- [ ] **Keep test and scan steps failing when they should**: capture the exit status before
  writing reports, as in the [test reports guide](../guide/test-reports.md).

## What next

- [Trust model](trust-model.md).
- [Supply chain](supply-chain.md).
