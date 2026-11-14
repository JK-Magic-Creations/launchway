# Testing the chart on kind

Everything here is for testing only. It installs the chart with two servers, two runners and
the development database in a throwaway [kind](https://kind.sigs.k8s.io/) cluster, runs a real
pipeline, and checks failover and rolling upgrades.

| File | What it is |
|---|---|
| `values-kind.yaml` | Chart values: the locally built image, the development database, no sign-in (`auth.allow_anonymous`), one project, step pods in `lw-pkg-steps` |
| `make-test-repo.sh` | Builds the test repository (branches `main`: two quick steps, the second reading a file the first wrote; `slow`: a 90-second step) as `demo-repo.tar.gz` |
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

The servers wait for the development database to start (each try is logged), so they should not
restart.

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

## 4. A pipeline, end to end, and run affinity

```sh
start() { curl -s -X POST -H 'X-Launchway-CSRF: 1' -d "{\"ref\":\"$1\"}" $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'; }
RUN=$(start main)
deploy/test/run-status.py $API $RUN --wait 120
curl -s $API/api/v1/runs/$RUN/steps/hello/logs     # "Starting pod lw-pkg-steps/launchway-run-..."
curl -s $API/api/v1/runs/$RUN/steps/check/logs     # "written by hello in pod ...": the file step 1 wrote
$K -n lw-pkg-steps get pods                         # the run's pods; removed within ~30s of success

# Repeatedly: every run succeeds, both steps on the same runner.
for i in 1 2 3 4 5; do deploy/test/run-status.py $API $(start main) --wait 180; done

# Two runs started together go to different runners.
R1=$(start main); R2=$(start main)
deploy/test/run-status.py $API $R1 --wait 180; deploy/test/run-status.py $API $R2 --wait 180
```

## 5. Losing a server mid-run

```sh
$K -n lw-pkg apply -f deploy/test/poller.yaml
RUN=$(curl -s -X POST -H 'X-Launchway-CSRF: 1' -d '{"ref":"slow"}' $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
deploy/test/run-status.py $API $RUN                 # wait until "slow" is running
$K -n lw-pkg delete pod $($K -n lw-pkg get pods -l app.kubernetes.io/component=server -o name | head -1)
$K -n lw-pkg logs poller | grep -c ' ok '; $K -n lw-pkg logs poller | grep -c FAIL   # expect 0 FAIL
# restart the port-forward if it was on the deleted pod, then:
deploy/test/run-status.py $API $RUN --wait 240      # succeeded, every step on attempt 1
```

## 6. Losing a runner mid-step

```sh
RUN=$(curl -s -X POST -H 'X-Launchway-CSRF: 1' -d '{"ref":"slow"}' $API/api/v1/projects/demo/runs | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
deploy/test/run-status.py $API $RUN                 # note the runner of the running "slow" step
$K -n lw-pkg delete pod <that runner's pod name, without the "runner-" prefix>
$K -n lw-pkg-steps get pods                         # the other runner starts a new pod for the run at once
deploy/test/run-status.py $API $RUN --wait 240      # succeeded; "slow" on attempt 2 and "finish" on the other runner
curl -s $API/api/v1/runs/$RUN/steps/slow/logs       # "This run moves here from runner ...; ... a fresh workspace"
sleep 40; $K -n lw-pkg-steps get pods               # no pods left, the deleted runner's included
curl -s $API/api/v1/runners                         # the deleted runner is gone (it said it was leaving)
```

A runner deleted with SIGTERM hands its step back and tells the server it is leaving, so the
run moves at once. A runner that vanishes without that (a lost node) would lose the step after
30 seconds and the run after 90, and would be listed as offline after two minutes.

## 7. A rolling upgrade

Restart the port-forward afterwards: its pod is replaced. Add `--set runner.kubernetes.cpu=60m`
to roll the runners too.

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

`lw-pkg-steps` is created by the chart and deleted on uninstall, with any step pods in it.
