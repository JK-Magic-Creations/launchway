# Watching and steering a Launch

See everything a delivery made, read its logs, and pause, restart or return it, from one page.

A **Launch** is one delivery of one Flight (codebase) to one Airfield: what a deploy step did, and what is running
because of it. Its page shows the live tree of every object the delivery owns, the strategy's steps and
where it is, how much real traffic the new version has, what the tests measured, and the logs of any pod.
From the same page you can steer it. Everything is read by a launch Station (runner) in the Airfield, not by the
server, and nothing here needs a second tool open.

Open it from a Sortie's deploy step (**Open this Launch**), or from the deployments views.

## What is on the page

The address holds the tab, the selected object and the log you chose, so a view can be bookmarked and
shared.

| Tab | What it shows |
|---|---|
| **Overview** | When something is wrong, the cause comes first ("Pathfinder (canary) pods are not healthy: container app is waiting: CrashLoopBackOff. Last log line: ..."), with buttons for the object and its logs. Then **what happens next** and who may step in, the strategy's steps with the current one marked and its elapsed and remaining time, the share of real traffic wanted and actual (a bar, and a table instead), the ReplicaSets with their role and replica counts, and the controls. |
| **Objects** | The tree: the application's workloads, then ReplicaSets (stable, Pathfinder, preview or active, old), Pods and their containers; analysis runs, their Jobs and the test Pods; Services (and which ReplicaSet each selects); routes with their weights; ConfigMaps and Secrets; service accounts and roles; horizontal pod scalers, disruption budgets, volume claims and network policies. Each object has its status, what the controller says, events (warnings first), and the live manifest. A pod that is not ready says why, from its status and events. |
| **Logs** | Any pod and container, all pods of a ReplicaSet, all pods with a role, or a test Job. **Stable and Pathfinder side by side**, or interleaved with the source named on every line. Follow (live), the earlier container of a restarted pod, wrap, search, a time range, download. |
| **Traffic and tests** | Where requests go (route weights), each analysis metric with its query, its threshold and every measurement with pass or fail, the verification of this Launch (every call a built-in health check made, with status, latency and a short excerpt of the answer), and the test pods with their output. |
| **History** | The commands given to the rollout, and what the launch Station saw. |

Every state is a word and a glyph as well as a color. The tree and the tabs work from the keyboard (arrow
keys, Home and End; Enter selects).

### What is shown, and what never is

The tree starts from the objects the Argo CD application lists (and the rollout the step names) and
follows owner references down. Objects outside the namespaces the Airfield lists are not read. Objects
that carry the application's label but are not rendered any more are shown as **left over**. Each object
says whether it is **new** or **changed** in this Launch.

A Secret shows its name, type and key names. Its values are never sent to the server. ConfigMap data,
manifests and logs pass through a redaction: values whose name looks like a secret, tokens, bearer and
basic credentials, passwords in text, private keys, and the values of the release's own Secrets (when the
launch Station may read them) are replaced by `***` before anything leaves the launch Station.

## Who may do what

| | Role |
|---|---|
| The tree, manifests | viewer of the Flight |
| Logs | operator of the Flight (on a protected Airfield (environment), from a binding that covers it) |
| Pause, resume, restart, scale; promote, promote fully, abort, retry | operator, likewise |

Every manifest and log request and every control is in the Logbook, with who asked, what object, and
the reason. The tree view is recorded once every few minutes per person, not at every refresh.

A button that cannot work is not shown: the page says why instead ("Scale is not offered: the autoscaler
shop manages the replica count"; "controls are off for this Airfield").

## Controls

| Action | What it does | Guard |
|---|---|---|
| Promote | Goes on past the pause the rollout waits at | Only at a pause; only if the rollout is still where it was shown |
| Promote fully | Skips the remaining steps and analyses | **A reason is required.** The page warns when an analysis failed or is running |
| Abort | Back to the stable version | Asks first |
| Retry | Starts an aborted rollout again | Only when aborted |
| Pause, Resume | Holds a rollout (or a Deployment) where it is | A reason is required; resume only for a pause a person made |
| Restart the pods | A rolling restart of the workload | A reason is required |
| Scale | Changes the replica count | A reason; between 1 and the Airfield's `scale_max`; not when an autoscaler manages it |
| **Abort and return to the last good release** | Aborts what is rolling out, then returns the service to the previous release through Git (the same as Return to base) | Operator; you type the service's name, and a reason |

Zero touch stays the default: the page says what will happen next and when without anyone clicking.

A restart or scale changes the live object, not Git. If Argo CD self-heals the application, it may put the
old value back; use the repository for a change that must stay.

## What each way of delivering offers

| Delivery | Tree | Logs | Steps and weights | Controls |
|---|---|---|---|---|
| Pathfinder with Argo Rollouts | yes | yes, stable and Pathfinder separately | the strategy's steps, desired and actual weight | all of the above |
| Blue-green with Argo Rollouts | yes (active and preview) | yes | preview up, tests, switch, old kept, scale down | all of the above |
| A plain Deployment | yes | yes | no steps or weights: Kubernetes replaces pods a few at a time | pause, resume, restart, scale; going back is Return to base |
| A StatefulSet, DaemonSet, Job or CronJob | yes | yes | none | restart (and scale for a StatefulSet) |

## From the command line and from an agent

```text
launchway rollout status RUN DEPLOYMENT
launchway rollout tree RUN DEPLOYMENT
launchway rollout logs RUN DEPLOYMENT --role canary --follow
launchway rollout restart RUN DEPLOYMENT --reason "stuck pod"
```

The MCP tools are `get_delivery`, `get_delivery_manifest`, `get_delivery_logs` (reads) and
`control_delivery` (changes a running service: it needs `confirm: true` and a reason). Words that come
from the cluster are returned as untrusted data. See the [command-line reference](../reference/cli.md#launchway-rollout).

## What the launch Station needs {#what-the-deploy-runner-needs}

Looking into a delivery is **off** until an administrator turns it on, in two places: the launch Station's
settings (`deploy.targets.NAME.inspect`) and its cluster rights (the chart value
`delivery.deployRunner.rbac.inspect`). The exact rights are listed in the
[chart values reference](../reference/helm-values.md). Without them the page says what to turn on.

## Not yet

- A diff of a manifest against the previous Launch's is not built: only the live manifest is shown.
- Requests per second, error rate and latency for the stable and the new version are shown from the
  verify step's metrics and the analysis runs' queries, not drawn from a metrics source of their own.
- "Abort the return" is not available: a return is a run of its own, which can be canceled while it
  waits, as any Sortie (run).
