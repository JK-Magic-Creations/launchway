# Testing before real traffic

Prove a new version where real users are not, and only then let them reach it.

The safe order for a canary or blue-green delivery is: start the new pods, call them through an address
real users do not use, and give the new version weight only after those calls passed. Launchway does this
with keys it already has, and warns when a pipeline would do it the other way round.

## The pattern

```yaml
steps:
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back: { repo: gitops, branch: main, changes: [ { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } } ] }
    rollout: { name: shop, namespace: shop, until: paused }   # stop at the rollout's first pause

  verify-new-pods:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    duration: 3m
    interval: 30s
    on_fail: abort
    metrics:
      - name: preview-answers
        source: checks
        check:
          url: http://shop-canary.shop.svc.cluster.local:8080/api/health
          headers: { Host: shop.example.com }        # a host or header real users do not use
          status: [200]
          body_contains: '"status":"ok"'
          max_latency: 300ms
          every: 2s
          success: 0.99

  promote:
    kind: rollout
    needs: verify-new-pods
    deployment: deploy-prod
    action: promote
```

The rollout itself starts at 0% in the repository: its first steps are `setWeight: 0` and a `pause: {}` (or,
for blue-green, `autoPromotionEnabled: false`), and its canary or preview Service exists. While it waits there,
the Launch page says **0% of real traffic; testing through** the Service. `until: paused` makes the deploy
step end at that pause; the verify step makes its calls; only the step that needs it promotes.

The calls are the built-in health check: N calls at a rate (`every`), each with assertions on status
(`status`), body (`body_contains`, `json`) and speed (`max_latency`), and the share that must pass
(`success`). The deploy runner makes them itself; no load tool is used. The Launch page's **Traffic and
tests** tab shows every call that failed with its status, latency and a short, redacted excerpt of what
came back. For a heavier test, a rollout's analysis can run a Job (a push test), and its output is shown
under the Job.

## What Launchway warns about

`launchway validate` (and the editor) warn, for a deploy step with a rollout:

- **live-traffic-unverified**: no verify step watches the deployment. Real traffic reaches the new version
  as soon as the rollout takes weight.
- **live-traffic-before-verify**: a verify step watches it, but the deploy step waits until the rollout is
  healthy (the default), which is after real traffic arrived. Set `until: paused`.

They are warnings: a pipeline that has them still runs. To say it is meant, write `unverified: true` under
`rollout`. `launchway validate --resolved` shows the choice.

## What is enforced, and what is not

Launchway can check the pipeline, and that is all it checks here: that a verify step comes before the
promotion, and that the deploy step stops at a pause. The 0% step itself lives in the rollout's manifest in
your Git repository, which the pipeline file cannot see, so a rollout that starts at 10% still sends 10% of real
traffic before the pause. The Launch page shows the real weight (from the traffic router, or computed from replicas)
so a mistake is visible, and an operator can abort from the same page.
