# Secrets

How pipelines name secrets, and how Stations (runners) look them up so the server never sees a value. Setting up stores is in [Secret stores](../howto/secrets.md).

A step names the secrets it needs. Each one becomes an environment variable:

```yaml
steps:
  push:
    image: alpine:3.22
    secrets:
      REGISTRY_PASSWORD: { store: vault, key: apps/harbor/robot, field: password }
      NPM_TOKEN:         { store: env,   key: NPM_TOKEN }
    run: echo "$REGISTRY_PASSWORD" | some-login
```

**The server never sees a secret value.** The pipeline, the server and the UI hold only the
reference (`vault:apps/harbor/robot#password`); the Station looks the value up just before the step
runs, inside the network that owns the secret, and replaces it with `***` in the step's logs and
outputs. Values shorter than 4 characters are not masked, and the log says so.

Each Station has its own stores:

| Type | `key` is | Settings |
|---|---|---|
| `env` | an environment variable of the Station process (not `LAUNCHWAY_*`) | none |
| `file` | a file name in a directory, such as a mounted Kubernetes secret; nothing outside it can be read | `dir` |
| `vault` | a path in a KV version 2 engine of HashiCorp Vault or OpenBao; `field` is required | `address`, `mount` (default `secret`), `namespace`, `ca_file`, and one of `token_env`, `token_file`, or `role` for Kubernetes auth (with `auth_path`, default `kubernetes`, and `jwt_file`, default the pod's service account token) |

A Station has only the stores you configure. The one exception is `launchway run` on your own
machine, which also has an `env` store so a pipeline can read your own environment. A server or
shared Station gets an `env` store only if you define one, because it lets pipelines read the
Station's Airfield (environment).

Any pipeline can ask for any key, so every store takes allow-lists, checked on the Station:
`projects` and `keys`, lists of patterns where `*` does not cross a `/`. An empty list allows
everything.

On Kubernetes, a step's Airfield (secrets included) is held in memory inside the Sortie's pod
while the step runs, where the Sortie's other containers could read it. Do not put containers you do
not trust in a pipeline that uses secrets.

The built-in Station reads its stores from `runner.secrets` in the server's configuration; `launchway
run` reads the same map from `--secrets-config FILE`, and `launchway Station` from either
`--secrets-config FILE` or the `secrets:` section of its `--config` file (see [Commands](commands.md)):

```yaml
runner:
  secrets:
    vault:
      type: vault
      address: https://vault.example:8200
      role: launchway          # Kubernetes auth
      projects: [payments, "web-*"]
      keys: ["apps/*/*"]
    mounted:
      type: file
      dir: /var/run/launchway-secrets
```

A secret that does not exist or may not be read fails the step, naming the store and key. A store
that cannot be reached (no answer, sealed, 5xx) is asked again for 30 seconds, and then the step is
handed back to be run again later. `launchway run` uses the project name `local`.
