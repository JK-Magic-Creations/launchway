# Testing the chart on kind

Everything here is for testing only. It installs the chart with two servers, two runners and
the development database in a throwaway [kind](https://kind.sigs.k8s.io/) cluster, runs a real
pipeline, and checks failover and rolling upgrades.

| File | What it is |
|---|---|
| `values-kind.yaml` | Chart values: the locally built image, the development database, one project, step pods in `lw-pkg-steps` |
| `make-test-repo.sh` | Builds the test repository (branches `main`: quick, `slow`: a 90-second step) as `demo-repo.tar.gz` |
| `git-server.yaml` | Serves that repository inside the cluster at `http://git-server:8080/demo.git` (BusyBox httpd, Git's dumb HTTP protocol) |
| `poller.yaml` | A pod that requests `/readyz` through the Service five times a second and logs `ok` or `FAIL` |
| `run-status.py` | Prints a run's steps, or waits for it to finish |

Every command names its context explicitly. Never point these at a real cluster.

## 1. Cluster and image

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman        # rootless Podman; omit for Docker
CTX=kind-launchway-test
kind create cluster --name launchway-test       # skip if it exists
K="kubectl --context $CTX"
TMP=$(mktemp -d)

make image VERSION=0.0.0-k8stest IMAGE=localhost/launchway:0.0.0-k8stest
podman pull docker.io/library/postgres:17.12-alpine
podman pull docker.io/library/busybox:1.37        # helper image, step image and test servers
podman save -o $TMP/launchway.tar localhost/launchway:0.0.0-k8stest
podman save -o $TMP/postgres.tar docker.io/library/postgres:17.12-alpine
podman save -o $TMP/busybox.tar docker.io/library/busybox:1.37
for f in launchway postgres busybox; do kind load image-archive $TMP/$f.tar --name launchway-test; done
```

## 2. Install

```sh
deploy/test/make-test-repo.sh $TMP
$K create namespace lw-pkg
$K -n lw-pkg create configmap demo-repo --from-file=$TMP/demo-repo.tar.gz
$K -n lw-pkg apply -f deploy/test/git-server.yaml
$K -n lw-pkg rollout status deploy/git-server

helm --kube-context $CTX install lw deploy/helm/launchway -n lw-pkg \
  -f deploy/test/values-kind.yaml --wait --timeout 5m
$K -n lw-pkg get pods
```

The servers may restart once or twice while the development database starts; that is
expected.

Without a database the install is refused:

```sh
helm --kube-context $CTX install nodb deploy/helm/launchway -n lw-pkg --dry-run=server
# Error: ... Launchway needs a PostgreSQL database shared by all servers. Set one of: ...
```

## 3. Both servers ready, both runners connected

```sh
for p in $($K -n lw-pkg get pods -l app.kubernetes.io/component=server -o name); do
  echo "${p#pod/}: $($K get --raw /api/v1/namespaces/lw-pkg/pods/${p#pod/}:8080/proxy/readyz)"
done

$K -n lw-pkg port-forward service/lw-launchway 18080:8080 &   # restart it if its pod goes away
API=http://127.0.0.1:18080
curl -s $API/api/v1/runners
```

## 4. A pipeline, end to end

```sh
RUN=$(curl -s -X POST -d '{"ref":"main"}' $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
deploy/test/run-status.py $API $RUN --wait 120
curl -s $API/api/v1/runs/$RUN/steps/hello/logs     # "Starting pod lw-pkg-steps/launchway-run-..."
$K -n lw-pkg-steps get pods                         # the run's pods; removed within ~30s of success
```

## 5. Losing a server mid-run

```sh
$K -n lw-pkg apply -f deploy/test/poller.yaml
RUN=$(curl -s -X POST -d '{"ref":"slow"}' $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
deploy/test/run-status.py $API $RUN                 # wait until "slow" is running
$K -n lw-pkg delete pod $($K -n lw-pkg get pods -l app.kubernetes.io/component=server -o name | head -1)
$K -n lw-pkg logs poller | grep -c ' ok '; $K -n lw-pkg logs poller | grep -c FAIL   # expect 0 FAIL
# restart the port-forward if it was on the deleted pod, then:
deploy/test/run-status.py $API $RUN --wait 240      # succeeded, every step on attempt 1
```

## 6. Losing a runner mid-step

```sh
RUN=$(curl -s -X POST -d '{"ref":"slow"}' $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
deploy/test/run-status.py $API $RUN                 # note the runner of the running "slow" step
$K -n lw-pkg delete pod <that runner's pod name, without the "runner-" prefix>
deploy/test/run-status.py $API $RUN --wait 240      # succeeded; "slow" on attempt 2, another runner
```

## 7. A rolling upgrade

```sh
T0=$(date -u +%T)
helm --kube-context $CTX upgrade lw deploy/helm/launchway -n lw-pkg \
  -f deploy/test/values-kind.yaml --set config.extraConfig.user=ci-admin --wait
until [ "$($K -n lw-pkg get pods -l app.kubernetes.io/component=server --no-headers | wc -l)" = 2 ]; do sleep 2; done
$K -n lw-pkg logs poller | awk -v t=$T0 '$1>=t' | grep -c FAIL        # expect 0
curl -s $API/api/v1/info                            # {"actor":"ci-admin",...} after re-forwarding
```

## 8. Clean up

```sh
kill %1                                             # the port-forward
helm --kube-context $CTX uninstall lw -n lw-pkg --wait
$K delete namespace lw-pkg lw-pkg-steps --ignore-not-found --wait
$K get ns lw-pkg lw-pkg-steps                       # NotFound
$K get clusterrole,clusterrolebinding,pv | grep -i -e launchway -e lw-pkg   # nothing
rm -r $TMP
```

`lw-pkg-steps` is created by the chart and deleted on uninstall; step pods that a lost runner
left behind (see deploy/README.md, "Known limitations") go with it.
