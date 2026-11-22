# Let Launchway build and deploy itself

This guide shows how an installation of Launchway builds its own container image and deploys it to
itself, through Git and Argo CD, with a stage in front (a sandbox instance) and a person approving
each step. It also says what to do when Launchway is down, and how to go back. It is for operators
who run Launchway on Kubernetes with Argo CD and a GitOps repository.

> **Verified** on 2026-11-20 against the `feat/self-deploy` branch: the pipeline file passes
> `launchway validate`; the chart renders `delivery.deployRunner.accept` and the Station (runner) loads it
> (`go test ./internal/config`). **Not verified:** a real run of the pipeline, an image built and
> pushed by it, a sandbox or dev deployment made by it, the break-glass steps on a live cluster.
> Treat each section as a plan until its game day has passed.

## The idea in one paragraph

Launchway never changes the cluster to deploy itself. Its pipeline builds an image, scans it, signs it
and pushes it by digest; then makes **one commit** in the GitOps repository that names the new digest
and the matching chart revision. From then on Argo CD and Argo Rollouts are in charge: they sync the
commit and run the Pathfinder (canary) with its analysis. Launchway only watches and records, and it can do so
because a deploy step holds no lease while it waits: replacing every Launchway pod does not lose the
deployment.

```text
push to main
  -> test, build the image (scan, sign, push by digest)
  -> approve  -> commit to GitOps (sandbox)  -> Argo CD + canary  -> verify 10 min
  -> approve  -> commit to GitOps (dev: THE SAME DIGEST) -> Argo CD + canary -> verify 15 min
  failure at any watched step: the rollback step reverts the commit (image and chart pin together)
```

## What you set up once

None of this is in the pipeline file, because a pipeline must not be able to grant itself it.

| Where | What | Notes |
|---|---|---|
| Depot (registry) | A robot account that may push **only** the repository of the Launchway image and its cache | Do not reuse a robot that can push everywhere. Turn on tag immutability for the repository: a tag is for people, the digest is what deploys. |
| Station (`runner.images`) | A builder for the Flight (codebase) `launchway`, a Depot with a push rule for that repository, the Station's signing key | One BuildKit daemon is one trust boundary: give this Flight a daemon of its own. See [Container images](../guide/images.md) and `deploy/buildkit/README.md`. |
| Station (`runner.secretStores`) | A `file` store holding the robot's name and password, allowed for the Flight `launchway` only | The credentials are read by the Station; step pods never see them. |
| Node | The BuildKit seccomp profile on the node that runs the daemon | `deploy/buildkit/seccomp-rootless-buildkit.json`; how it reaches the node is your decision (host configuration or the Security Profiles Operator). |
| Server (`delivery.environments`) | Airfields (environments) `launchway-sandbox` and `launchway-dev` (dev `protected: true`) | The names in `.launchway/pipeline.yaml`; rename both together. |
| Deploy Station (`delivery.deployRunner`) | `applications` naming exactly the two Argo CD Applications; `rolloutNamespaces` the two namespaces; `accept: { projects: [launchway], environments: [...], executors: [deploy] }` | `rbac.patchApplications: false` and `refresh: false`: Argo CD syncs by itself, so Launchway asks it for nothing and holds read access only. `accept` needs the chart with `delivery.deployRunner.accept` (this branch). |
| Deploy Station (`repos`) | The GitOps repository with `paths` listing the four files the pipeline edits | A token that can push to that repository only; `paths` is enforced by the Station, not by Git. |
| Verification sources | `prometheus` and `loki`, for Flight `launchway` | The pipeline's verify steps read pod readiness and restarts, and the server's logs. |
| GitOps repository | An Argo CD `PreSync` job that dumps the database | Launchway cannot dump its own database from a pipeline. The dump runs before the new pods exist, and a failed dump fails the sync, so nothing changes. |
| GitOps repository | A second, small Launchway instance (the sandbox) with its own database, namespace and credentials | Never share credentials with dev. |

## First bootstrap: build by hand, deploy with Argo CD

The release that introduces self-deploy cannot deploy itself, and neither can a release that raises
the database's minimum schema (the `schema_min` value). Do the first one, and those, by hand:

1. **Back up the database** (`docs/howto/backup-restore-upgrade.md`) and note the running image digest.
2. **Build the image** from the commit with the Dockerfile, passing the commit so the labels are true:

   ```sh
   podman build --build-arg VERSION=$(git rev-parse --short=8 HEAD) \
     --build-arg REVISION=$(git rev-parse HEAD) -t registry.example.com/launchway/launchway:$(git rev-parse --short=8 HEAD) .
   podman push --digestfile digest.txt registry.example.com/launchway/launchway:$(git rev-parse --short=8 HEAD)
   ```

   Check `org.opencontainers.image.revision` with `skopeo inspect`; it must be the commit, not `unknown`.
3. **Commit to the GitOps repository**: `image.tag` and `image.digest` in the instance's values file, and
   the chart's `targetRevision` in its Application, in **one** commit (they must match).
4. **Let Argo CD sync** and watch the Rollout: `kubectl argo rollouts get rollout <name> -n <namespace> -w`.
   No `helm upgrade`, no `kubectl apply`.
5. Check `/readyz` and `/api/v1/info` (`schema.database` equals `schema.code`).

## Self-deploy, step by step

1. A push to `main` starts the Sortie (run). Pull requests build and scan but never push or deploy.
2. `check-code`, `test` (with 4 GiB asked for), `build-ui`, `build-binary` and `gate-pipeline` run as script steps.
3. `image` builds with the Dockerfile on the builder, scans (a finding at `high` or worse stops it before
   anything is pushed), pushes by digest and signs. Its outputs are the digest and the pushed name.
4. `approve-sandbox`: read the image tab (digest, signature, SBOM, scan), then approve.
5. `deploy-sandbox` commits three edits in one commit and waits until Argo CD has synced it and the Rollout is
   healthy. `verify-sandbox` watches for ten minutes. A failure runs `rollback-sandbox`.
6. `approve-dev`: a person other than the one who pushed approves, naming the **same digest** the sandbox ran.
7. `deploy-dev` and `verify-dev` do the same on the instance that runs the pipeline; the Rollout's own Pathfinder
   analysis is the verdict, Launchway only records it.

While dev's servers are replaced the Sortie keeps going: the Sortie is an event log, and the deploy step decides from
observations posted by the deploy Station to whichever server is up. Replace the deploy Station **separately**
from the servers (its own release, its own namespace), and keep it one version behind, so the observer is
never the version under test.

## When Launchway itself is down (break-glass)

Nothing here needs Launchway.

1. **Look**: `kubectl argo rollouts get rollout <name> -n <namespace>`, and the Argo CD Application's status.
2. **Revert in Git**: `git revert` the deploy commit in the GitOps repository. It restores the image and the
   chart pin together. Argo CD syncs the old template and the Rollout does a Pathfinder of the old image.
3. **In a hurry**: `kubectl argo rollouts abort <name> -n <namespace>` (or `undo`) **and** the revert in the same
   minute. Without the revert, Argo CD sets the cluster back from Git.
4. **No image?** Build and push by hand as in the bootstrap, until the old build chain is retired. Do not retire
   it before a break-glass game day has passed.
5. Afterwards: cancel the stuck run so the deploy lock is released, and write the intervention in your change record.

## Return to base (rollback) {#rollback}

Going back means going back **with the image, never with the database.** Every migration since schema 7 is
additive, and an older build runs against a newer schema, so reverting the deploy commit is safe. Restoring a
dump throws away every Sortie, approval, deployment record and audit row written since it was taken, which is
exactly what describes the failure.

| Situation | Do | Never |
|---|---|---|
| The Pathfinder was aborted by its analysis | The Return to base step reverts the commit; or `git revert` by hand | Restore the database; scale the servers to zero |
| The new pod will not start | Wait for the Rollout's progress deadline to abort, then revert | `kubectl delete` the Rollout; edit the live Deployment |
| Promoted, then the verify step failed | The Return to base step reverts; the old image goes through the same Pathfinder | Roll back in the cluster only |
| A migration is slow or locks | Let the Rollout abort; revert | Restore a dump |
| A release raised `schema_min` above the old build's schema | Not self-deployed. By hand, with the owner, from the pre-deploy dump | |
| The revert does not apply (a later commit touched the same lines) | A person resolves it; the deploy says it needs one | Force-push |

## What does not exist yet

The pipeline uses only what is on `main` plus the chart setting in this branch. These are missing, and nothing
pretends otherwise:

- **Image steps cannot ask for memory.** `resources` applies to script steps. The builder's own memory limit
  (set where the daemon is deployed) is what protects the image build from being killed.
- **No pre-flight gates against the running server**: `launchway version --json`, `server --check-config`
  and a test of the previous image against the new schema do not exist. `gate-pipeline` checks only the new
  build against the new pipeline file.
- **No pre-deploy backup step.** Argo CD's PreSync job does it; the pipeline cannot confirm the dump exists.
- **An approval's message is text**: it cannot show the digest or the schema change; the person reads the image tab.
- **No `/metrics` on the server**, so verify steps use Kubernetes and log signals, not request rates or error
  ratios, and no Pathfinder probe that creates a Sortie through the Pathfinder Service.
- **No reconciler** comparing Git, Argo CD and the running version; **no Station drain** (runs in flight on a replaced Station start the step again); **no signature check at deploy time**.
- **The observer shares a release with the servers** unless you give the deploy Station its own release.
