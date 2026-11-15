# Argo CD and Argo Rollouts test environment (kind)

Everything here is for testing Launchway's deploy steps against real Argo CD and Argo Rollouts
in a throwaway [kind](https://kind.sigs.k8s.io/) cluster. Every script takes the context from
`--context NAME` or `KUBE_CONTEXT` (default `kind-launchway-test`) and **refuses any context
whose name does not start with `kind-`**. Never point these at a real cluster.

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman            # rootless Podman; omit for Docker
kind create cluster --name launchway-test           # skip if it exists

make test-argo                    # check context, up.sh, then the Go tests in internal/deploy/integration
deploy/test/argo/up.sh            # install or update everything and wait until ready (about 40 s from scratch)
deploy/test/argo/down.sh          # remove everything listed under "What gets installed"
eval "$(deploy/test/argo/git-url.sh)"   # port-forward the Git server; sets LAUNCHWAY_TEST_ARGO_GIT_URL etc.
deploy/test/argo/seed.sh "$LAUNCHWAY_TEST_ARGO_GIT_URL"   # recreate the test repository (about 6-15 s)
```

`up.sh` seeds the repository only when it does not exist yet; `LAUNCHWAY_TEST_ARGO_RESEED=1` forces it.

## Files

| File | What it is |
|---|---|
| `lib.sh` | Shared helpers: context check, pinned versions and checksums, port-forward, waits |
| `up.sh`, `down.sh` | Install / remove |
| `seed.sh` | (Re)creates `gitops.git` from `fixtures/`, adds a push webhook to Argo CD, applies the AppProject and Applications, syncs the manual ones, waits for all to be Synced and Healthy |
| `git-url.sh` | Starts a port-forward to the Git server on a free local port and prints shell assignments |
| `test-env.sh` | Context check plus the settings the Go tests use (run by `make test-argo`) |
| `git-server.yaml` | The Git server (namespace `lw-git`) |
| `argocd/` | Workload namespaces, the repository credentials Secret, AppProject `lw-test`, the Applications |
| `fixtures/` | The repository content (`apps/manual` is copied from `apps/demo` by `seed.sh`) |
| `deployer-rbac.yaml` | Least-privilege RBAC for the deploy runner, as service account `argocd/lw-deployer` |
| `admission-policy.yaml` | ValidatingAdmissionPolicy limiting that account's Application patches to `.operation` and the refresh annotation |

## Pinned versions

| Component | Version | How |
|---|---|---|
| Argo CD | v3.3.7 | `https://raw.githubusercontent.com/argoproj/argo-cd/v3.3.7/manifests/install.yaml`, sha256 `bf1844394bf6213a60d316c2e3b0229495eb1d97689347f427bb2db69c1b510e` |
| Argo Rollouts | v1.8.3 | `https://github.com/argoproj/argo-rollouts/releases/download/v1.8.3/install.yaml`, sha256 `6f4f581dab41450ca623a03d246156fa7fa79901f7f01693c5df66f7557fc145` |
| Git server | Gitea 1.27.2 | `docker.io/gitea/gitea:1.27.2-rootless@sha256:c7953964c6e8fe532cf7fc3f3efdea1d0b161d7859c47e04f5e05489d494dea0` |
| Workload image | busybox 1.36.1 and 1.37.0 | `docker.io/library/busybox:1.36.1` (the seed version) and `:1.37.0`; both run `httpd` on 8080 |
| Analysis Job image | busybox 1.37.0 | `docker.io/library/busybox:1.37.0` |

The two install manifests are downloaded (not vendored: 2.8 MB) by pinned URL, checked against
the sums in `lib.sh`, and cached in `~/.cache/launchway-test-argo`. A mismatch stops the install.
They are applied with server-side apply (the CRDs are too big for client-side apply). The kind node
pulls the images itself; with no network, load them with
`KIND_EXPERIMENTAL_PROVIDER=podman kind load docker-image --name launchway-test <image>`.

## Argo CD settings

`up.sh` sets in `argocd-cm`: `timeout.reconciliation: 10s` and `timeout.reconciliation.jitter: 0s`
(default 180 s plus jitter), and restarts the controller and repo server once when they change.

**Argo CD v3.3.7 does not re-read Git for an idle Application on that timer.** The controller
only compares again when something triggers a refresh: a change to one of the app's live
resources, a refresh annotation, a webhook, or a spec change. In testing, an app whose resources
did not change was not compared for 17 minutes with a 10 s timeout, and a pushed commit was never
picked up. (In `controller/appcontroller.go`, the informer's periodic resync delivers an update
with an unchanged resourceVersion, and the handler returns before requesting a refresh.) The short
timeout still makes those event-driven refreshes re-read Git.

So `seed.sh` adds a Gitea push webhook to `https://argocd-server.argocd.svc/api/webhook`
(commit to Synced in about 4 s). Without it, a writer must set the annotation
`argocd.argoproj.io/refresh: normal` after pushing (Argo CD removes it when done; about 1 s).
`LAUNCHWAY_TEST_ARGO_WEBHOOK=0 seed.sh ...` leaves the webhook out, to test that path.

## Git server

Gitea, rootless, SQLite on a 1 GiB volume (`local-path`), no SSH, smart HTTP with basic auth.

| | |
|---|---|
| In the cluster | `http://git.lw-git.svc:8080/lwtest/gitops.git` |
| From the host | `kubectl port-forward -n lw-git svc/git :8080` (`git-url.sh` does it and prints the URL) |
| User / password | `lwtest` / `lwtest-not-a-secret` (test-only; Secret `lw-git/git-credentials`, and `argocd/repo-lw-gitops` for Argo CD) |
| Push | `git -c http.extraHeader="Authorization: Basic $(printf lwtest:lwtest-not-a-secret \| base64 -w0)" push ...` |

The repository is private and created by the first push. `seed.sh` deletes it through Gitea's API
(`DELETE /api/v1/repos/lwtest/gitops`) and pushes a new one, so every seed starts a fresh history.

### Settings for the Go tests

| Variable | Default |
|---|---|
| `LAUNCHWAY_TEST_ARGO_CONTEXT` | set by `make test-argo` from `KUBE_CONTEXT`, default `kind-launchway-test` |
| `LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE` | `lw-git` |
| `LAUNCHWAY_TEST_ARGO_GIT_SERVICE` | `git` (port 8080) |
| `LAUNCHWAY_TEST_ARGO_GIT_USER` | `lwtest` |
| `LAUNCHWAY_TEST_ARGO_GIT_PASSWORD` | `lwtest-not-a-secret` |
| `LAUNCHWAY_TEST_ARGO_RESEED` | `0`; `1` makes `up.sh` recreate the repository |
| `LAUNCHWAY_TEST_ARGO_WEBHOOK` | `1`; `0` makes `seed.sh` leave out the webhook |

The repository path is `lwtest/gitops.git`. The tests port-forward the Service themselves.
`git-url.sh` also prints `LAUNCHWAY_TEST_ARGO_GIT_URL`, `LAUNCHWAY_TEST_ARGO_GIT_AUTH_HEADER`
and `LAUNCHWAY_TEST_ARGO_GIT_PF_PID` (kill that PID when done).

## The repository and the Applications

| Application | Path | Namespace | Sync | What |
|---|---|---|---|---|
| `demo` | `apps/demo` | `lw-demo` | automated, selfHeal, no prune | Rollout `demo` with analysis, Service, AnalysisTemplate |
| `demo-manual` | `apps/manual` | `lw-demo-manual` | none: a deploy must request a sync | copy of `apps/demo` |
| `plain` | `apps/plain` | `lw-demo` | automated, selfHeal | Deployment `plain`, Service |
| `demo-multi` | `apps/multi/workload` + `apps/multi/config` | `lw-demo-multi` | none | two sources: Rollout `multi` (no analysis) and ConfigMap `multi-config` |
| `demo-wref` | `apps/workloadref` | `lw-demo-wref` | automated, selfHeal | Rollout `wref` with `workloadRef` to Deployment `wref` (0 replicas) |

All are in AppProject `lw-test` (sync windows are added there by tests; `seed.sh` removes them).
No Application has the resources finalizer. Workload namespaces enforce Pod Security "restricted".

### Rollout `demo` and its one-line edits

Canary, 2 replicas, no traffic router: `setWeight: 50`, `pause: { duration: 5s }`, analysis
(`demo-verdict`), `setWeight: 100`. `progressDeadlineSeconds: 30` with `progressDeadlineAbort: true`.

| Scalar | Values | Effect |
|---|---|---|
| `image: docker.io/library/busybox:1.36.1` | `1.36.1` / `1.37.0` | new canary |
| env `MODE` | `serve` / `crash` | `crash`: the container exits; the canary never gets ready and aborts at the progress deadline |
| analysis arg `verdict` | `"pass"` / `"fail"` | `fail`: the analysis Job fails and the Rollout aborts |
| analysis arg `delay` | `"0"` | seconds the analysis Job sleeps first (to observe or act mid-analysis) |
| `pause` | `{ duration: 5s }` / `{}` | `{}` is an indefinite pause that waits for promote |

A change to `verdict`, `delay` or `pause` alone does not start a canary (the pod template is the
same; only the generation moves). Combine it with an image or `MODE` change.

### Measured times (kind on a laptop, images cached)

| Scenario | Time |
|---|---|
| `up.sh` from an empty cluster, including seed | 40 s |
| `seed.sh` (all five apps Synced and Healthy on the seed commit) | 6-15 s |
| Commit to sync: webhook / refresh annotation / polling only | 4 s / 1 s / never for an idle app (once 25 s, when another event triggered a refresh) |
| Happy canary, sync to Healthy | about 9 s (pause 5 s + analysis about 3 s) |
| `verdict: fail`, push to aborted | 11-12 s |
| `MODE: crash`, push to aborted | 33 s (progress deadline 30 s) |
| Revert of the bad commit to Healthy | 2-5 s (see below) |

After a revert the pod template matches the stable ReplicaSet again: the Rollout skips straight to
the last step (`currentStepIndex` = number of steps), creates no AnalysisRun, removes
`status.abort` and `status.abortedAt`, and is Healthy on the previous version within seconds.

## Deployer identity (RBAC and admission policy)

`up.sh` applies `deployer-rbac.yaml` and `admission-policy.yaml`. Tests act as the deploy runner
with `kubectl --as=system:serviceaccount:argocd:lw-deployer` (or the `Impersonate-User` header).

- Applications: `get, list, watch, patch` limited by `resourceNames` to the five apps; AppProject
  `lw-test`: `get`. LIST and WATCH work only with a single-name field selector
  (`fieldSelector=metadata.name=demo`); without it they are forbidden.
- Workload namespaces: `rollouts` and `analysisruns` `get, list, watch`; `rollouts/status`
  `patch` for the named Rollout; in `lw-demo-wref` also `deployments` `get` for `wref`.
- Nothing on pods or secrets. `kubectl patch --subresource=status` first GETs `rollouts/status`,
  which this role does not allow; a client that sends the PATCH directly works.
- The admission policy denies any change by that account other than `.operation` and the
  annotation `argocd.argoproj.io/refresh` (spec, status, labels, other annotations, finalizers,
  owner references). Other users, and the Argo CD controller, are not affected.

## What gets installed

Namespaces: `argocd`, `argo-rollouts`, `lw-git`, `lw-demo`, `lw-demo-manual`, `lw-demo-multi`,
`lw-demo-wref`.

Cluster-scoped objects (all removed by `down.sh`):

| Kind | Names |
|---|---|
| CustomResourceDefinition | `applications.argoproj.io`, `applicationsets.argoproj.io`, `appprojects.argoproj.io`, `analysisruns.argoproj.io`, `analysistemplates.argoproj.io`, `clusteranalysistemplates.argoproj.io`, `experiments.argoproj.io`, `rollouts.argoproj.io` |
| ClusterRole | `argocd-application-controller`, `argocd-applicationset-controller`, `argocd-server`, `argo-rollouts`, `argo-rollouts-aggregate-to-admin`, `argo-rollouts-aggregate-to-edit`, `argo-rollouts-aggregate-to-view` |
| ClusterRoleBinding | `argocd-application-controller`, `argocd-applicationset-controller`, `argocd-server`, `argo-rollouts` |
| ValidatingAdmissionPolicy and binding | `launchway-deployer-applications` |
| PersistentVolume | the Git server's volume, provisioned by `local-path` with reclaim policy Delete (goes with its claim) |

No webhooks are installed (neither upstream manifest has admission webhooks).
