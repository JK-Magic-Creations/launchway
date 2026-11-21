# Deploy with approval, rollout control and rollback

Release a service through Argo CD as an Argo Rollouts canary: Launchway commits the new version to
a GitOps repository, stops at the canary's pause, smoke-tests the canary, asks a person, promotes
it to 100%, and rolls back by itself when the smoke test fails. For developers and operators who
have run a [first pipeline](first-pipeline.md) and want to see delivery end to end.

> **Verified** on 2026-11-19 against commit f717337: every command from step 2 on, in order, on a
> kind cluster (Kubernetes in rootless Podman) with Argo CD v3.3.7 and Argo Rollouts v1.8.3 as
> `deploy/test/argo/up.sh` installs them, with other namespace, application and port names.
> **Not verified:** step 1 on a new machine (the cluster already existed), Docker instead of
> Podman, a cluster other than kind, the Helm chart's deploy runner.

You need a throwaway Kubernetes cluster with Argo CD, Argo Rollouts and a Git server. The
repository's test environment makes one with kind in a few minutes. Everything else runs on your
machine. It takes about an hour, most of it reading.

```mermaid
flowchart LR
  B["build"] --> D["deploy: commit, sync, wait for the canary's pause"]
  D --> S["canary-smoke"]
  S -->|"passed"| A["approve: a person decides"]
  A --> P["promote: rollout to 100%"]
  S -->|"failed"| R["rollback: abort, then revert the commit"]
  P -->|"failed"| R
```

## What you need

- Launchway built from source at `~/launchway`: follow [Install on a laptop](../howto/install-laptop.md).
  This page assumes the binary is at `~/launchway/bin/launchway`.
- [kind](https://kind.sigs.k8s.io/), `kubectl`, Podman (or Docker), `git`, `openssl`, `curl` and
  `jq`.
- Free local ports 8080 (Launchway) and 3000 (the Git server, forwarded).
- About 4 GB of free memory for the cluster.

Words used here: an **environment** is a place a service is deployed to (here `staging`), served
by a **target** (a cluster); a **deploy runner** is the process that changes the target, and a
**runner pool** is the token it uses; a **deployment** is one deploy step's change and everything
that guards it. See the [glossary](../glossary.md).

## 1 Make the cluster

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman     # leave out with Docker
kind create cluster --name launchway-test
~/launchway/deploy/test/argo/up.sh
```

`up.sh` installs Argo CD, Argo Rollouts and a Gitea Git server (user `lwtest`, password
`lwtest-not-a-secret`, test-only values) into the cluster whose context is `kind-launchway-test`,
and refuses any context whose name does not start with `kind-`. It also adds its own test
applications, which you can ignore. See
[the test environment's README](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/test/argo/README.md).

```sh
kubectl --context kind-launchway-test -n argocd get pods
kubectl --context kind-launchway-test -n argo-rollouts get pods
```

Every pod is `Running`.

## 2 Copy the tutorial files

```sh
export PATH="$HOME/launchway/bin:$PATH"
cp -r ~/launchway/examples/tutorials/deploy ~/deploy-tutorial
cd ~/deploy-tutorial
ls -F
```

```text
cluster/  deploy-runner.yaml  gitops/  launchway.yaml  shop/
```

| Path | What it is |
|---|---|
| `gitops/` | The GitOps repository: the shop's Rollout and Services |
| `cluster/` | The namespaces, and the Argo CD Application with its repository credentials |
| `shop/` | The shop's own repository, with the pipeline |
| `launchway.yaml` | The server's configuration |
| `deploy-runner.yaml` | The deploy runner's configuration |

Every command below runs in `~/deploy-tutorial`. Make the namespaces: `shop` for the service and
`launchway-steps` for the pipeline's step pods, both enforcing Pod Security "restricted":

```sh
kubectl --context kind-launchway-test apply -f cluster/namespaces.yaml
```

## 3 Push the GitOps repository

The shop is BusyBox's web server, serving `shop <version>` on `/` and `ok` on `/healthz`. Its
`gitops/apps/shop/rollout.yaml` is an Argo Rollouts canary that stops at a pause with no duration,
until something promotes it:

```yaml
  strategy:
    canary:
      canaryService: shop-canary   # only the new version's pods
      stableService: shop          # only the current version's pods
      steps:
        - setWeight: 50
        - pause: {}                # wait here until something promotes the rollout
        - setWeight: 100
```

The version is one line, which the deploy step will change:

```yaml
          env:
            - name: VERSION
              value: "1.0.0"
```

And it has a bug planted on purpose, so that you can see a rollback: a version ending in `-broken`
serves no `/healthz`.

Forward the Git server to port 3000 and push:

```sh
kubectl --context kind-launchway-test -n lw-git port-forward svc/git 3000:8080 >/dev/null &
cd gitops
git init -b main
git add -A
git commit -m "The shop, version 1.0.0"
git push http://lwtest:lwtest-not-a-secret@127.0.0.1:3000/lwtest/shop-gitops.git main
cd ..
```

Gitea creates the private repository `lwtest/shop-gitops` on the first push. Keep the
port-forward running: the deploy runner pushes through it.

## 4 Create the Argo CD Application

`cluster/application.yaml` gives Argo CD the repository's credentials and creates the
Application `shop`, which syncs `apps/shop` into the `shop` namespace by itself:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: shop
  namespace: argocd
spec:
  project: default
  source:
    repoURL: http://git.lw-git.svc:8080/lwtest/shop-gitops.git
    targetRevision: main
    path: apps/shop
  destination: { server: https://kubernetes.default.svc, namespace: shop }
  syncPolicy:
    automated: { selfHeal: true, prune: false }
```

```sh
kubectl --context kind-launchway-test apply -f cluster/application.yaml
kubectl --context kind-launchway-test -n argocd get application shop
kubectl --context kind-launchway-test -n shop get rollout shop
```

After a few seconds:

```text
NAME   SYNC STATUS   HEALTH STATUS
shop   Synced        Healthy
NAME   DESIRED   CURRENT   UP-TO-DATE   AVAILABLE   AGE
shop   2         2         2            2           15s
```

## 5 Configure and start the server

`launchway.yaml` declares the environment, the runner pool that may serve its target, and the
built-in runner, which runs steps as pods in the cluster so the smoke test can reach the canary:

```yaml
listen: 127.0.0.1:8080
data: data

projects:
  - name: shop
    repo: shop
    branch: main

environments:
  - name: staging
    target: kind                       # a deploy runner serves this target
    projects: [shop]

runner_pools:
  - name: deploy-kind
    token_env: LAUNCHWAY_POOL_DEPLOY_KIND_TOKEN
    targets: [kind]

runner:
  executor: kubernetes
  kubernetes:
    context: kind-launchway-test
    namespace: launchway-steps
```

Only a pool's token can take deploy work, and only for the pool's targets: the shared runner
token, which runners that run anyone's code hold, cannot. A pool token is at least 32 characters.

Make the shop's repository, a pool token, and start the server with the token in its environment:

```sh
cd shop
git init -b main
git add -A
git commit -m "Release the shop with a canary"
cd ..
openssl rand -hex 32 > pool-token
LAUNCHWAY_POOL_DEPLOY_KIND_TOKEN=$(cat pool-token) launchway server --config launchway.yaml
```

```text
  Launchway f717337 is listening on http://127.0.0.1:8080 (no sign-in: anyone who can reach it has full access)
  1 project(s), data in /home/you/deploy-tutorial/data, SQLite database /home/you/deploy-tutorial/data/launchway.db
...
level=INFO msg="runner started" name="laptop (built in)" executor="kubernetes (launchway-steps)" concurrency=2 server=http://127.0.0.1:8080
```

Leave it running.

## 6 Start the deploy runner

The deploy runner is `launchway runner` with a `deploy:` section. It never runs a script: it
commits to the GitOps repository, asks Argo CD to sync if it must, watches the Application and the
Rollout, and gives the rollout commands. `deploy-runner.yaml`:

```yaml
server: http://127.0.0.1:8080
name: deploy-kind
data: deploy-runner-data
secrets:
  files:
    type: file
    dir: secrets
deploy:
  targets:
    kind:
      kubernetes: { context: kind-launchway-test }   # never the current context
      argocd_namespace: argocd
      applications: [ { projects: [shop], names: [shop] } ]
      rollout_namespaces: [shop]
  repos:
    gitops:
      url: http://127.0.0.1:3000/lwtest/shop-gitops.git          # as this machine reaches it
      argocd_url: http://git.lw-git.svc:8080/lwtest/shop-gitops.git   # as the Application writes it
      username: lwtest
      credentials: { store: files, key: gitops-password }
      paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
```

- `applications`, `rollout_namespaces` and `paths` are allow-lists: project `shop` may deploy only
  the Application `shop`, watch rollouts only in `shop`, and change only files under `apps/shop/`.
- `url` and `argocd_url` differ because your machine reaches Git through the port-forward and
  Argo CD reaches it inside the cluster. The runner matches the Application by `argocd_url`.
- The Git password comes from a secret store, never from this file.

In a second terminal, in `~/deploy-tutorial`:

```sh
export PATH="$HOME/launchway/bin:$PATH"
mkdir -p secrets
printf lwtest-not-a-secret > secrets/gitops-password
chmod 600 secrets/gitops-password
LAUNCHWAY_RUNNER_TOKEN=$(cat pool-token) launchway runner --config deploy-runner.yaml
```

```text
INFO deploy runner started name=deploy-kind targets=[kind] server=http://127.0.0.1:8080
```

> [!NOTE]
> This runner uses your kind admin context, which is fine for a throwaway cluster. In a real
> cluster, run the deploy runner with the Helm chart (`delivery.deployRunner`), which gives it
> only the access it needs: see [Install on Kubernetes](../howto/install-kubernetes.md).

Open **http://127.0.0.1:8080/environments**: `staging` says *observed by deploy-kind*.

## 7 Read the pipeline

`shop/.launchway/pipeline.yaml`:

```yaml
pipeline: shop

inputs:
  version: { type: string, default: "1.1.0", description: The version to release }

steps:
  build:
    image: docker.io/library/busybox:1.37.0
    run: |
      # A real pipeline builds and pushes an image here. The tutorial
      # releases a version number instead.
      echo "version=$LAUNCHWAY_INPUT_VERSION" >> "$LAUNCHWAY_OUTPUTS"

  deploy:
    kind: deploy
    needs: build
    environment: staging
    service: shop
    version: { output: build.version }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - file: apps/shop/rollout.yaml
            set: spec.template.spec.containers[0].env[0].value
            value: { output: build.version }
    rollout: { name: shop, namespace: shop, until: paused }   # stop at the canary's pause

  canary-smoke:
    needs: [build, deploy]
    image: docker.io/library/busybox:1.37.0
    run: |
      url=http://shop-canary.shop.svc
      want="shop $LAUNCHWAY_NEEDS_BUILD_VERSION"
      # The canary Service follows the new pods within a few seconds.
      for i in $(seq 1 15); do
        got=$(wget -qO- "$url/" || true)
        [ "$got" = "$want" ] && break
        sleep 2
      done
      echo "The canary says: $got"
      [ "$got" = "$want" ]
      wget -qO- "$url/healthz"

  approve:
    kind: approval
    needs: canary-smoke
    deployment: deploy
    message: The canary passed its smoke test. Promote the shop to 100%?

  promote:
    kind: rollout
    needs: approve
    deployment: deploy
    action: promote
    until: healthy
    timeout: 10m

  rollback:
    kind: rollback
    deployment: deploy
    needs: [promote]
    abort_first: true
```

| Step | What it does |
|---|---|
| `build` | Stands in for building an image: it outputs the version to release. With an [image step](build-and-sign-image.md) you would pin the image by digest instead (`image:` and `digest: { output: image.digest }` in `changes`) |
| `deploy` | Commits the new `VERSION` to `apps/shop/rollout.yaml` (`write_back`), lets Argo CD sync, and with `until: paused` ends when the rollout reaches its pause, with the canary running |
| `canary-smoke` | An ordinary step, run as a pod, that tests the canary through `shop-canary` |
| `approve` | Waits for a person. `deployment: deploy` shows the live rollout beside the question |
| `promote` | A rollout step: it promotes past the pause and waits until the rollout is healthy |
| `rollback` | Runs only if the deployment was made and a step it needs did not succeed. `abort_first` aborts the canary at once, then reverts the commit |

`rollback` needs `promote`, so a failure anywhere on the way (the smoke test, a rejected approval,
the promotion) leaves `promote` not succeeded and the rollback runs. See
[Deployments](../guide/deployments.md) for every key.

## 8 Release version 1.1.0

Go to **Projects**, press **Start a run** on `shop`, keep `version` at `1.1.0` and press
**Start run**. (Or: `curl -s -X POST -H 'X-Launchway-CSRF: 1' -d
'{"ref":"main","inputs":{"version":"1.1.0"}}' http://127.0.0.1:8080/api/v1/projects/shop/runs`.)

In about half a minute `build`, `deploy` and `canary-smoke` have succeeded and the run waits for
approval. The `deploy` step's log:

```text
Deploy runner deploy-kind, target kind, application shop
Deploying shop 1.1.0 to staging: committing to gitops on main
apps/shop/rollout.yaml: spec.template.spec.containers[0].env[0].value set to 1.1.0
Committed 24966d9 to main: Deploy shop 1.1.0 to staging
Not asking Argo CD to sync: shop syncs by itself
Sync: not needed: the application syncs by itself
Done; waiting for Argo CD
```

And `canary-smoke`'s:

```text
The canary says: shop 1.1.0
ok
```

Click the `deploy` step to see the **deployment panel**: what Argo CD and the rollout report now,
the canary's steps with the current one, the observations so far, and buttons to control the
rollout.

![The deployment panel of the deploy step while the run waits for approval: staging on target kind, application shop, version 1.1.0, commit 24966d99 to gitops on main, holding staging's lock; observed now, the rollout is paused at step 2 of 3; Argo CD synced, health Suspended; the rollout's steps weight 50%, pause (current) and weight 100%; Promote, Promote full and Abort buttons; and the observations step by step from waiting for Argo CD to the pause](../images/tutorials/deploy-panel-paused.png)

The deployment holds `staging`'s lock: another run of the shop waits until this one is done.
You could press **Promote** here; instead, decide the approval. Open **Waiting for a decision** and click the
question (or press **Decide** on the run). The clearance step shows what it guards, with an
optional comment; press **Go** (labeled **Approve** in the plain set).

The `promote` step promotes the rollout and waits until it is healthy; `rollback` is skipped
(*every step it needs succeeded*). The run succeeds, and the panel of `promote` shows the command
it gave:

```text
COMMANDS
Promote  by step promote  applied
for the rollout at canary step 2, paused  pod template c67899b5b
```

Now open **Environments**:

![The Environments page: service shop in environment staging, observed by deploy-kind, runs version 1.1.0 since a few seconds ago, deployed by run 1, Argo CD sync Synced and health Healthy](../images/tutorials/deploy-environments-deployed.png)

## 9 Release a broken version and watch the rollback

Start another run (**Projects**, **Start a run** on `shop`), this time with `version` set to
`1.2.0-broken`. The canary starts, but its
`/healthz` is missing, so `canary-smoke` fails:

```text
The canary says: shop 1.2.0-broken
wget: server returned error: HTTP/1.1 404 Not Found
Failed with exit code 1 after 100ms
```

`approve` and `promote` are skipped, and `rollback` runs. Its log:

```text
Deploy runner deploy-kind, target kind, application shop
Rolling back shop in staging: reverting deployment dep_06gzdj757vps0b12zcqg on main
Aborting rollout shop/shop first (abort_first), if it is still on 54d9b5cb57
Applied: abort rollout shop/shop on 54d9b5cb57
Committed bccb4c1 to main: Roll back shop in staging to 1.1.0
Reverted 06a23dbeb9 with bccb4c1e34 on main
Not asking Argo CD to sync: shop syncs by itself
Sync: not needed: the application syncs by itself
Done; waiting for Argo CD
```

The abort took the canary's traffic away within seconds; the revert is what lasts, because Git
named the broken version until then. The run fails (its smoke test did), and its deployment says
**Rolled back**:

![The run of version 1.2.0-broken: it failed at canary-smoke with HTTP 404; staging shows shop 1.1.0 to 1.2.0-broken, Rolled back; the rollback step's panel says the rollback step reverted the commit and the environment was healthy again afterwards, with the version 1.2.0-broken that ran 1.1.0 before, the commit and the commit that reverted it, and the observations from the pause to healthy](../images/tutorials/deploy-rollback.png)

The GitOps repository has the whole story, as ordinary commits:

```sh
git clone -q http://lwtest:lwtest-not-a-secret@127.0.0.1:3000/lwtest/shop-gitops.git history
git -C history log --format='%h %an  %s'
```

```text
bccb4c1 Launchway  Roll back shop in staging to 1.1.0
06a23db Launchway  Deploy shop 1.2.0-broken to staging
24966d9 Launchway  Deploy shop 1.1.0 to staging
dd45276 You  The shop, version 1.0.0
```

**Environments** still shows `1.1.0`, deployed by run 1, with run 2 rolled back in its history:

![The Environments page after the rollback: shop in staging runs 1.1.0 from run 1; run 2, 1.2.0-broken, Rolled back; Synced and Healthy; the history lists run 2 rolled back and run 1 deployed](../images/tutorials/deploy-environments-rolled-back.png)

## 10 Clean up

Stop the server and the deploy runner with Ctrl+C, then:

```sh
kill %1                                   # the port-forward, if started from this shell
kubectl --context kind-launchway-test delete -f cluster/application.yaml
kubectl --context kind-launchway-test delete -f cluster/namespaces.yaml
rm -rf ~/deploy-tutorial
```

To remove the whole test environment, run `~/launchway/deploy/test/argo/down.sh`, or delete the
cluster with `kind delete cluster --name launchway-test`.

## If something goes wrong

| What you see | Cause and fix |
|---|---|
| The `deploy` step waits in *queued* | No deploy runner serves target `kind`: check the runner is running with the pool's token |
| `a valid runner token is required (HTTP 401)` in the deploy runner's output | The token is not the pool's (`LAUNCHWAY_POOL_DEPLOY_KIND_TOKEN` of the server) |

## What you learned

- Environments and runner pools live in the server's configuration; a deploy runner with the pool's
  token is the only thing that touches Git and the cluster for them.
- A deploy step commits a version to a GitOps repository and judges "deployed" from what Argo CD
  and Argo Rollouts report, never from the commit alone.
- `until: paused`, a smoke step, an approval naming the deployment and a `kind: rollout` step give
  a canary a test and a person before it reaches 100%.
- A rollback step with `abort_first` stops a bad canary at once and reverts its commit, and the
  Environments page and Git both show what happened.

## What next

- [Build, scan, sign and push a container image](build-and-sign-image.md), then deploy it by
  digest.
- [Policy gates and waivers](policy-gates.md): require tests, a clean scan and an approval before
  `deploy` may run.
- [Deployments](../guide/deployments.md) in the guide: sync windows, queues, `on_cancel`,
  protected environments.
- [Install on Kubernetes](../howto/install-kubernetes.md): run the deploy runner with the Helm chart.
