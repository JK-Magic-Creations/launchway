# Deployments

Deploy steps with Argo CD and Argo Rollouts: GitOps write-back, sync, health, rollout control and rollback.

A deploy step changes what runs in an environment. It commits the new version to a GitOps
repository, asks Argo CD to sync if it must, and waits until the application (and its Argo
Rollouts rollout, if it has one) is healthy on that version. A rollback step reverts that commit
when a step guarding the deployment does not succeed.

```yaml
steps:
  build:
    container: kaniko
    run: ./ci/build.sh                 # writes tag=1.4.2 to $LAUNCHWAY_OUTPUTS

  deploy-prod:
    kind: deploy
    needs: build
    when: { branch: main }
    environment: prod                  # configured on the server: the lock and the record
    service: shop                      # default: the project's name
    version: { output: build.tag }     # what "current version" shows; default { run: short_sha }
    timeout: 30m                       # the whole step, commit to healthy; default 30m
    argocd:
      application: shop
      write_back:
        repo: gitops                   # a repository the deploy runner's configuration names
        branch: main
        changes:
          - file: apps/shop/rollout.yaml
            image: team/shop           # every reference to this image; none at all is an error
            tag: { output: build.tag }
          - file: helm-values/shop.yaml
            set: image.tag             # a YAML path; comments and layout are kept
            value: { output: build.tag }
      sync:
        request: auto                  # auto (only if the application does not sync by itself) | always | never
        resources:                     # sync, and judge, only these; default: the whole application
          - { kind: Rollout, name: shop, namespace: shop }
        prune: false
        windows: wait                  # wait | fail, while a sync window holds the sync; never overridden
      health: resources                # application | resources; default resources when listed
    rollout: { name: shop, namespace: shop }   # also wait until this rollout is healthy

  smoke:
    needs: deploy-prod
    image: curlimages/curl:8.10.1
    run: ./ci/smoke.sh https://shop.example

  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]                     # the steps that guard the deployment
    timeout: 15m                       # default 15m
```

- **Pin by digest**: `digest: { output: image.digest }` in place of `tag:` writes
  `repository@sha256:…` (see [Container images](images.md)).
- **Values** are written in place, or are `{ output: step.key }` (an output of a step this step
  needs), `{ run: sha | short_sha | branch | tag | number | commit_time }` or `{ input: name }` (a
  [run input](pipelines.md#run-inputs); `environment` may be `{ input: name }` too, of a choice input). There is
  no expression language.
- **"Deployed" means**, in one observation: the revision Argo CD has synced, for the source from
  the write-back repository, includes the commit; the judged resources (or the whole application)
  are Synced and Healthy; and the rollout, if one is named, is Healthy with its stable ReplicaSet on
  the new pod template and its current spec observed. Missing or stale observations are never a
  pass. A failed sync of the commit, a Degraded application or resource, or an aborted or Degraded
  rollout (named with its failed analysis run and metric) fails the step; so does the deadline,
  saying what was last observed. The rollout's own analysis runs are the verdict, shown read-only.
- **A deployment that changes nothing** (Git already holds the version) commits nothing, and is
  still synced and verified.
- **Sync windows** are never overridden. The deploy runner reads the application's AppProject and
  never asks for a sync a window does not allow: with `windows: wait` the step waits (until its
  deadline) for a window to allow it; with `windows: fail` it fails, saying which window. Argo CD
  itself would hold such a sync and then, when the window ends, mark it done without applying
  anything, so asking anyway is never safe. An application that syncs by itself and is held by a
  window shows as held.
- **Argo CD learns of the commit** from the refresh annotation the deploy runner sets right after
  it (Argo CD does not look at Git again by itself for an application nothing has changed in), or
  from a Git webhook to Argo CD if you have one; `refresh: false` on a target turns the annotation
  off.
- **The rollback rule**: a rollback step runs if and only if its deployment was made and one of the
  steps it needs did not succeed (it failed, or was skipped or canceled because something failed);
  otherwise it is skipped as not needed. It reverts the deployment's commit (never a forced reset),
  asks Argo CD to sync if it must, and waits until the revert is synced and healthy. A revert that
  does not apply (later commits changed the same lines) fails the step and leaves Git alone; the
  deployment then says it needs a person. A deployment that changed nothing has nothing to revert.
- **One deployment at a time**: each service of a project has one lock per environment, taken when
  its deployment opens and released when the deploy step, its rollback step and every step on a
  needs path between them are done. A deploy step that finds it held waits. With `queue: newest`
  (the default), a run waiting for the lock is skipped as superseded when a newer run of the same
  service starts waiting; with `queue: all` they deploy in turn. `supersede: true` never cancels a
  run that holds an open deployment: it finishes, and the newer run waits.
- **Where deploy steps do not run**: draft runs, pull-request runs and `launchway run`. A deploy step
  is never reused by a re-run, a re-run cannot start inside a deployment (re-run from its deploy
  step), and a run that made a deployment is not resumed.
- **Validation** says what is wrong and on which line: a value from a step that is not needed, a
  rollback that does not need its deploy step, two deploy steps of one service and environment that
  could run at once, and, against the server's configuration (in `/api/v1/validate` and when a run is
  created), an unknown environment or a project the environment does not allow.
- **Not yet**: syncing to a revision without a commit, a "roll back to…" button that starts a run,
  pausing a rollout, and `kind: analysis` arrive in later releases.

## Rolling back after the run succeeded

The rollback step above undoes a deployment that fails its own guards. To roll a service back later, after
a deployment succeeded and the run is finished, ask for a rollback of its own: a run that reverts the
deployments after the version you choose, newest first. See [Rollback after success](rollback-after-success.md).

## Controlling a rollout: promote, abort, retry

A canary can stop at a pause and wait: for a smoke test of the canary, for a person, or both.

```yaml
  deploy-prod:
    kind: deploy
    # ... as above ...
    rollout: { name: shop, namespace: shop, until: paused }   # end at the first indefinite pause
    on_cancel: abort                   # leave (default) | abort: abort the rollout if the run is canceled

  canary-smoke:
    needs: deploy-prod
    image: curlimages/curl:8.10.1
    run: ./ci/smoke.sh http://shop-canary.shop.svc

  approve-full:
    kind: approval
    needs: canary-smoke
    deployment: deploy-prod            # shows the live rollout; rejected by itself if it is aborted
    message: "The canary passed its smoke test. Promote shop to 100%?"

  promote:
    kind: rollout
    needs: approve-full
    deployment: deploy-prod
    action: promote                    # promote | promote-full | abort | retry
    until: healthy                     # healthy | paused (the next indefinite pause); not for abort
    timeout: 30m

  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true                  # abort the rollout (seconds), then revert (what lasts)
```

- **An indefinite pause** is a pause step without a duration, a blue-green pause without automatic
  promotion, or an analysis that ended inconclusive. A deploy step watching until healthy says what
  to do there with `rollout.pauses`: `wait` (the default: someone presses Promote on the run page),
  `promote` (Launchway promotes each such pause once; never an inconclusive analysis, which waits
  for a person) or `fail`. With `until: paused` the step ends at the first one, and a `kind: rollout`
  step naming the deployment must carry it on.
- **A rollout step** acts on its deployment's rollout and then watches it: until healthy, until the
  next indefinite pause after the one it promoted, or, for `abort`, until the rollout is aborted. It
  gives its command from the first fresh observation, so the command expects what the rollout was
  really doing then.
- **Commands.** Promote, Promote full, Abort and Retry are commands, given by a person on the run
  page (or `POST /api/v1/runs/{run}/deployments/{deployment}/promote`, `promote-full`, `abort`,
  `retry`, with an optional `{"comment": "..."}`), by a rollout step, by `pauses: promote`, or by
  `on_cancel: abort`. Each records what it expects of the rollout: the deployment's pod template and
  the canary step it was at. A deploy runner serving the target applies it only if the rollout is
  still there; a rollout already past that point records "already applied", and one now on another
  pod template (a change outside Launchway replaced it) records "not applied". So a second click on
  the same pause, on any server, is refused naming who already asked, and an abort only ever stops
  this deployment's version. A command no deploy runner has taken can be withdrawn
  (`POST .../commands/{command}/withdraw`); an abort withdraws the ones not taken yet and goes first.
  Commands given while no deploy runner reaches the cluster wait, and are applied when one does.
  Every command, withdrawal and refusal is in the Logbook (`deployment.promote` and so on).
- **Who may.** A command needs the operator role on the run's project from a binding that covers
  the deployment's environment (see `environments:` under [Roles](access.md#roles)). On an environment marked
  `protected: true`, the person who started the run may not promote or retry it (unless
  `approvals.allow_self` lets them approve their own runs); anyone with the operator role there may
  abort it. An approval that names a deployment needs the same role on its environment.
- **The deployment** stays open, holding the environment's lock, until the deploy step, its rollout,
  rollback and linked approval steps, and every step on a needs path between them are done. It is
  "deployed" only if its rollout got to healthy; a deployment left at a pause, or aborted, is
  "failed" unless a rollback reverted it. A deploy step that stops at a pause does not change the
  environment's current version; the rollout step that gets it healthy does.
- **Rolling back with `abort_first`** aborts the rollout first if it is still on the deployment's pod
  template, then reverts the commit and waits until the previous version is healthy. Git still
  names the bad version until the revert lands, which is why the revert is never skipped.
- **`on_cancel: abort`** gives an abort command when the run is canceled by hand while the
  deployment is open; the deployment ends as abandoned. Git still names the version: an application
  that syncs by itself keeps it, aborted, until someone reverts it.
- **With run inputs, loops and cleanup steps.** The deploy step's version and tag may be run inputs.
  Steps inside the deployment may be loops (`for_each`, a smoke test per region, say); rollout steps
  and approvals that name a deployment are never repeated, keep or fetch no artifacts and report
  nothing. Neither may need, directly or not, a step that runs after failures
  (`when: { upstream: failed | always }`), and such an approval takes no upstream rule itself: a
  cleanup step's success would let a failed check be promoted past. Make the cleanup step need them
  instead.
- **Not yet**: promoting past an analysis step that is still running, and promoting a rollout paused
  through its spec (`spec.paused`), which needs a change to the spec Launchway does not make.

**Who does the work.** Deploy steps are done by a **deploy runner**: `launchway runner` with a
`deploy:` section, in or near the cluster, with the token of a **runner pool**. Only a pool's token
can take deploy work, and only for the targets the pool may serve; the shared runner token, which
build runners running anyone's code hold, cannot claim a target or report that a deployment is
healthy. The deploy runner talks to the cluster through the Kubernetes API with its own identity;
it never runs a script, and the servers hold no cluster credentials. It reads Applications,
Rollouts and AnalysisRuns; it changes only an Application's operation (to request a sync) and its
refresh annotation, and, for commands, the status of the rollouts it is allowed to control. One
deploy runner observes each target at a time; another takes over within a lease period when it
stops. It also observes, every 30 seconds and at most 100 per target, the services nothing is
deploying, so the Environments page shows when what runs differs from the last deployment.

```yaml
# server: launchway.yaml
environments:
  - { name: prod, target: prod-cluster, projects: [shop, "web-*"], queue: newest }
runner_pools:
  - { name: deploy-prod, token_env: LAUNCHWAY_POOL_DEPLOY_PROD_CLUSTER_TOKEN, targets: [prod-cluster] }

# deploy runner: runner.yaml (launchway runner --config runner.yaml, with the pool's token)
server: https://launchway.example
deploy:
  targets:
    prod-cluster:
      kubernetes: { in_cluster: true }     # or { kubeconfig: ..., context: ... }; never the current context
      argocd_namespace: argocd
      applications: [ { projects: [shop], names: [shop] } ]
      rollout_namespaces: [shop]
  repos:
    gitops:
      url: https://git.example/team/gitops.git
      argocd_url: http://git-http.git.svc:3000/team/gitops.git   # as Applications write it, if different
      credentials: { store: vault, key: ci/gitops, field: token }     # a name, never a value
      paths: [ { projects: [shop], files: ["apps/shop/**", "helm-values/shop.yaml"] } ]
secrets:
  vault: { type: vault, address: https://vault.example:8200, role: launchway-deploy }
```

Pool tokens are at least 32 characters, and every server must have the same environments and
pools. With the Helm chart, `delivery.environments` and `delivery.deployRunner.*` run a deploy
runner in the cluster with the least access it needs: get, list and watch on the named
Applications (and patch, which a ValidatingAdmissionPolicy limits to the operation and the refresh
annotation), get on AppProjects (for their sync windows), get, list and watch on rollouts and
analysis runs in the listed namespaces, and nothing on pods or secrets. To let it apply commands,
name the rollouts in `delivery.deployRunner.rbac.controlRollouts` (`{ shop: [shop] }`): it then
gets patch on `rollouts/status` for exactly those, and a second admission policy holds the patch to
the fields promote, promote full, abort and retry set.

**The run page** shows a deployment panel for deploy and rollback steps: environment, application,
version and previous version, the commit and its revert, sync and health, the rollout's steps with
the current one, weights and pauses, its analysis runs with each metric's counts, replicas, and the
timeline of what was observed, with "observed 14s ago by runner X" (stale data is greyed, never
green). **The Environments page** shows each service by environment: the current version, since
when and from which run, the lock holder and the runs waiting, the last deployment's state, and its
history; protected environments; and, for a service nothing is deploying, what was last seen of
it and whether it drifted: "OutOfSync" (Argo CD says what runs differs from Git), "live differs
from the last deployment" (another commit or pod template runs), or the application is gone. When
nothing can be observed the page says why: no deploy runner serves the target, the runner cannot
reach the cluster (with its error), or Argo CD has not compared the application for minutes. The
deployment panel shows the buttons a person may use, the reasons for those they may not, and the
commands given, with their state. API: `GET /api/v1/environments`,
`GET /api/v1/deployments?environment=&project=&service=`, `deployments` in a run, and
`GET /api/v1/runs/{run}/deployments/{deployment}/controls`.

Deploy steps need schema 11; rollout steps, `until: paused`, `pauses`, an approval's
`deployment`, `abort_first`, `on_cancel: abort`, commands, `protected:` and `environments:` on access
bindings need schema 14. Use them only once every server and runner is upgraded, and take them out
before rolling back (see [docs/development/migrations.md](../development/migrations.md)).

## When a deploy fails: why, and what happened next

A failed deploy, rollout or rollback step always says why. In the run API the step carries `reason` (one
plain sentence) and `cause`: its `kind` (`analysis_failed`, `rollout_failed`, `sync_failed`, `timeout`,
`script_exit`, `refused`, `runner_lost` or `command_failed`), the evidence and `next`, what Launchway did
about it ("Rolled back to v1.2.3 by rollback-dev."). For a failed analysis the evidence is the analysis
run with each metric: what it checks, its counts, each measurement with its time and message, and for a
job metric the job's name and namespace and the end of its log. The same fields reach MCP (`get_run`) and
`launchway runs show`.

A deploy runner reads a failed job's log only when an administrator sets
`delivery.deployRunner.rbac.readPods`; otherwise the run says where the log is and that the runner may not
read it.

## Commands tell the truth

A rollout command has a visible life: requested, taken by a runner, then applied or **failed** with the
reason, shown on the buttons' row and in the run's timeline. The Logbook keeps the request
(`deployment.promote` and the like) and the outcome (`deployment.command_outcome`, `ok` or `failed`) as
separate entries. Each deploy runner checks, against the cluster's authorizer, what it may do to the
rollouts it watches and reports it with its observations; when it may not change a rollout's status, the
buttons show unavailable with the words to fix it: list the rollout in
`delivery.deployRunner.rbac.controlRollouts`. Nothing is offered that is known beforehand to fail.

## What you may do: the `actions` list


The server decides, once per resource, what a person may do now. A run's `actions` list (cancel, resume,
rerun) has for each action its name, whether it is `available`, the plain `reason` when not, `instead` (the
action that works), what role it needs (`who`), and for a risky action the `risk` and a `suggestion`.
Risky actions are never blocked for being risky: the interface shows the consequence, asks for
confirmation and goes ahead.

A deployment, and each service of an environment, reports its `rollback` action the same way: whether the
service can be rolled back and to what (`targets`), or why not and what would help. See
[Rollback after success](rollback-after-success.md).

## Rollout control does not need the chart's admission policy

Whether the buttons work is decided by the deploy runner's own self-check, not by how the cluster is
configured. A runner that may patch the status of a rollout (listed in `delivery.deployRunner.rbac.controlRollouts`)
offers promote, promote full, abort and retry. The chart's field-limiting admission policy
(`delivery.deployRunner.admissionPolicy.enabled`) is optional: where an installation manages that policy
itself, or runs without it, control still works as long as the runner reports the right. Without the
right, the buttons show unavailable and say which one is missing.
