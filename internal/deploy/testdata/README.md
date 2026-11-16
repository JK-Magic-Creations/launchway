# Golden status samples

Captured from Argo CD v3.3.7 and Argo Rollouts v1.8.3 on kind (Kubernetes 1.37), with the test
environment in `deploy/test/argo` (2026-11-15). Each file keeps `apiVersion`, `kind`, selected
metadata (name, namespace, uid, generation, resourceVersion, creationTimestamp, labels,
annotations without `last-applied-configuration`, ownerReferences), `spec`, `status`, and for
Applications the top-level `operation`. AnalysisRun files are `v1` Lists holding only the runs
the scenario is about.

Types worth knowing: a Rollout's `status.observedGeneration` is a **string** (`"22"`), and so is
`status.workloadObservedGeneration` (`"1"`), which is present only on the `workloadRef` Rollout
(absent on Rollouts with a pod template). `metadata.generation` is an integer.

| File | Scenario |
|---|---|
| `analysisruns-earlier-deployments.json` | the runs Argo Rollouts kept in lw-ctl (control tests) from revisions 18 to 30, three of pod template 6cccdd46d8, none of revision 34 |
| `analysisruns-failed.json` | the Failed AnalysisRun of rollout-aborted-analysis.json (verdict fail) |
| `analysisruns-running.json` | the Running AnalysisRun of rollout-analysis-running.json (delay 20) |
| `analysisruns-successful.json` | the Successful AnalysisRun of the happy canary (s1) |
| `analysisruns-terminated.json` | an AnalysisRun ended by promote mid-analysis: phase Successful, message "Run Terminated", spec.terminate true |
| `application-degraded.json` | demo after MODE crash: Synced, health Degraded (Rollout aborted) |
| `application-multi-source.json` | demo-multi (two sources, manual), synced to HEAD, Synced and Healthy |
| `application-operation-blocked-window.json` | demo-manual, operation patched while a deny sync window was active: phase Running, message "Sync operation blocked by sync window" |
| `application-operation-running.json` | demo-manual, sync operation requested by patching .operation, phase Running (message still the previous operation's) |
| `application-outofsync.json` | demo-manual after a commit (invalid Service port) before any sync: OutOfSync; previous operation a partial sync |
| `application-progressing.json` | demo during the happy canary: Synced, health Progressing |
| `application-suspended-timed-pause.json` | demo during the canary's timed 5s pause: health Suspended |
| `application-sync-failed.json` | demo-manual, sync to a commit with Service port 70000: operation Failed, Service SyncFailed |
| `application-synced-healthy.json` | demo (automated, selfHeal), Synced and Healthy |
| `rollout-aborted-analysis.json` | demo, bump to 1.36.1 with verdict fail: analysis step failed, Rollout aborted (Degraded) |
| `rollout-aborted-manual.json` | demo, aborted by the status patch {"status":{"abort":true}} at the indefinite pause |
| `rollout-analysis-running.json` | demo, indefinite pause promoted, at step 2 with analysis (delay 20) Running |
| `rollout-degraded-crash.json` | demo, MODE crash: canary never ready, progressDeadlineSeconds 30 with progressDeadlineAbort -> aborted, Degraded |
| `rollout-healthy-revision-34.json` | ctl (lw-ctl), back on pod template 6cccdd46d8 at revision 34, Healthy (the earlier runs above are not its) |
| `rollout-healthy.json` | demo, fully promoted on 1.37.0 (pod template, no workloadRef) |
| `rollout-paused-indefinite.json` | demo, step 1 is pause {} (indefinite), waiting for promote |
| `rollout-paused-timed.json` | demo, same bump, at step 1 pause {duration: 5s} |
| `rollout-progressing.json` | demo, tag bump 1.36.1->1.37.0, canary at step 0 (setWeight 50) scaling up |
| `rollout-workloadref-healthy.json` | wref (lw-demo-wref), workloadRef to Deployment wref scaled to 0, Healthy |
