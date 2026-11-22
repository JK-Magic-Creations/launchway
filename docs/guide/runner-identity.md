# Stations (runners) on other machines and clusters {#runners-on-other-machines-and-clusters}

Ramps (Station (runner) pools), join tokens, Station identities and the limits a Station sets for itself.

A Station only ever connects out to the server, over HTTPS, so it can sit in any network that can
reach the server: another machine, another cluster, a customer's data center. Each Station belongs
to a **pool**, and a pool says whose Flights' work its Stations may take.

## Connecting a Station {#connecting-a-runner}

Someone who manages Stations (the role `runner_admin` names on every Flight; admin by default)
makes a **join token** for a pool, on the Stations page (**Add a Station**), with
`launchway runners join-token --pool web`, or with `POST /api/v1/runner-join-tokens`. It is shown
once, stored only as a hash, lasts an hour by default (at most 7 days) and enrols one Station unless
it says more. On the Station's machine:

```sh
launchway runner --server https://ci.example.com --join lwj_… --config /etc/launchway/runner.yaml
```

The Station makes an Ed25519 key, which never leaves it (`<data>/identity/key`, readable by the
Station's user only), registers the public half, and gets a Station ID from the server. From then on
it starts without the join token, using its identity; the join token is used up. In a container,
pass `LAUNCHWAY_JOIN` instead of `--join`. In Kubernetes with the chart, put a join token in a
Secret and set `runner.join.existingSecret`: each pod enrols when it starts, so make the token
ephemeral (each Station is revoked when it leaves) with enough uses for every pod start. The page
shows the commands for each, ready to copy.

The Stations page shows each Station's pool, identity (key fingerprint and age, who made its join
token), version, OS and architecture, labels, what its `accept:` section allows, and what it is
running. **Revoke** refuses it at once on every server; the steps it held are queued again for
other Stations. **Rotate key** makes it move to a new key at its next request (Stations also rotate
every 30 days). Every join, rotation, revocation and refusal is in the Logbook.

The shared Station token (`LAUNCHWAY_RUNNER_TOKEN`, `--token`) and deploy pools' tokens keep
working as before, and Stations using them are marked on the Stations page: a shared token gives no
identity of its own, so the server cannot tell such Stations apart, trusts the Station ID each one
gives, and anyone who has the token can take that pool's work. Enrol them, then set
`require_identity: true` on the pool to refuse the shared token.

## Pools

```yaml
# launchway.yaml
runner_pools:
  - name: web                # build runners for the web team's projects
    kind: build
    projects: ["web-*"]
    events: [push, manual]   # optional: only runs started this way (not pull requests, say)
  - name: default            # the shared token's pool; listed only to limit it
    kind: build
    projects: ["*"]
    require_identity: true   # refuse the shared token (turn the built-in runner off first)
  - name: deploy-prod        # a deploy pool, as in Deployments
    token_env: LAUNCHWAY_DEPLOY_POOL_TOKEN
    targets: [prod-cluster]
```

Without the default pool listed, it serves every Flight no other build pool serves. A Station is
only ever given steps of Sorties (runs) its pool serves; the server checks it when it looks for work and
again when it grants it, whatever the Station says. A pipeline may choose among the pools that serve
its Flight, and ask for labels the Station must have:

```yaml
runner: { pool: gpu, labels: [cuda] }   # the whole pipeline: a run's steps share one workspace
```

Labels come from the join token and from the Station (`labels:` in `runner.yaml`, or `--labels`).
A Sortie that no Station may take says why on its page: no pool serves the Flight, the pool it asks
for does not, or none of the Stations seen lately has the labels or accepts it.

## Proxies and private certificates

The Station uses `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, or `proxy:` and `no_proxy:` in
`runner.yaml`, which win. `ca_file:` adds certificates to the system's, for a server or a proxy
whose certificate a private authority signed, such as one that inspects TLS. Stations prove who they
are inside the request, not with client certificates, so this works through proxies that inspect
TLS. A Station waits for work in requests of up to 25 seconds; a proxy must allow idle requests of
at least 30.

## What a Station trusts the server with {#what-a-runner-trusts-the-server-with}

Whoever controls the server decides what runs on the Stations: which commit, which script, which
image, which secrets a step asks for. A Station's **`accept:`** section is its own limit on that,
checked on the Station before anything runs (no clone, no secret looked up, no container started).
The server cannot change it, because it lives only in the Station's file. A job it does not allow is
refused: the step fails without retries, saying why, and the refusal is audited.

```yaml
# runner.yaml
accept:
  projects: [shop, "web-*"]
  repositories: ["https://git.example.com/shop/*"]   # the run's and every library's
  images: ["registry.example.com/*", "docker.io/library/golang@sha256:*"]
  images_by_digest: false
  executors: [podman, buildkit]                      # buildkit for image steps, deploy for deploy runners
  environments: [staging]                            # deploy runners
  pull_requests: false
  drafts: false                                      # pipeline text that is not in Git
  max_timeout: 2h
  env_deny: [LD_PRELOAD, BASH_ENV, ENV, "GIT_*"]
```

Every Station also refuses, whatever its settings: IDs from the server that do not look like the
server's own (they become paths); on a Station on another machine, a Sortie whose source is a directory
of that machine (only `launchway run` does that); and an enrolled Station does not fall back to
running steps directly on its host when no Podman or Docker is found, unless `executor: host` is
chosen.

What `accept:` does **not** stop: a server that has been taken over can still run any script it
likes for a Flight, repository and image the Station accepts, with that Station's network reach and
the secrets its stores give those Flights; it can choose which accepted commit runs, and replay an
old one. Keep each Station's `accept:`, secret stores and network as narrow as its pool's work, and
give production its own pool.

What the server checks of Stations: each enrolled Station's requests carry a short-lived access
token (15 minutes) that it gets by signing a 60-second assertion with its key; each assertion is
accepted once, by any server. A step's lease belongs to the Station that took it: its logs, results,
Cargo (artifacts) and heartbeats are refused from any other Station. A Station can only leave as itself, and
only hears about runs of its pool's Flights.

Station identities and join tokens need schema 20, and build pools and the pipeline key `runner:` need
schema 21: use them once every server and Station is upgraded, and take them out before returning to base
(see [docs/development/migrations.md](../development/migrations.md)).
