# Automatic resources: a design for review

Status: proposal, with stage 0 and stage 1 built (see "Stage 1 as built"
below). The rest is not built. It is written for review with the owner.

## The goal

Nobody should have to think about what a step needs. Launchway measures what
each step really uses, reserves that plus headroom, gives it back when the
step ends, raises it by itself when a step runs out, and packs as many builds
as fit. `resources:` in a pipeline file stays, as an optional override.

**The tie-breaker for every trade-off in this document, in the owner's
words: it is better to have slower builds than an unstable platform.** When
two choices conflict, the one that keeps the platform up wins, and the build
waits, is slowed, or is retried within its bounds.

Two things matter more than speed. A build must never become unstable because
of resources. And a build that runs for 30 minutes or an hour must never loop
without progress: it either moves forward or it stops, with a plain reason.

## What went wrong, so the design fixes the right things

1. A Station makes one pod per run, with one container per image. Each
   container is sized for the hungriest step that uses that image, and held
   for the whole run. The pipeline of this repository reserved 8448Mi per pod
   while it used about 1.1Gi.
2. Two Stations each took steps of one run, so the run held two pods. The
   namespace quota (16Gi) could not hold a third.
3. A Station missed heartbeats, the server took the leases back, and the pod
   of the abandoned attempt kept its quota. The retries waited for room that
   their own run held. (Fixed in this branch: the pod is released when the
   lease is lost, a restarted Station sweeps its old pods, and the lease now
   lasts 90 seconds with a heartbeat every 15.)
4. The message said "8448Mi already requested". It read like a step that ran
   out of memory. (Fixed: it now says what the run has reserved, across how
   many pods, and what must be released.)
5. **Shared containers.** Steps that use the same image run in the same
   container (`internal/engine/resources.go`, `runContainers`: "a container
   gets the largest of what its steps ask for, and every step in it may use
   that much"). Two steps of one stage with the same image, `test` and
   `security-go`, ran at the same time in one container with one 8Gi limit.
   Both compiled the module from an empty cache, together they went past 8Gi,
   and the kernel killed both (exit 137). A step's limit is not its own today.
   The pipeline file was worked around by ordering the two steps. The product
   must not rely on that.

## What exists now (Part A)

- Each step attempt is measured from its container's cgroup and the result is
  stored in `step_usage` (migration 54), shown by `launchway runs show`, the
  API (`usage` on each step) and the Sortie page.
- Kubernetes: a small shell loop runs in the step's container through the same
  exec the Station already uses. It reads `memory.current`, `memory.peak` and
  `cpu.stat` (cgroup v1: the `memory/` and `cpuacct/` files) once at the start,
  every second, and once at the end. It needs no extra permission and no
  metrics server. The cluster metrics API was rejected: it needs a metrics
  server and a role, and its figures are averaged over 15 to 60 seconds, which
  hides the peak that causes a kill.
- Podman and Docker: the step runs under a small wrapper that writes the same
  readings to a file in the step's directory. The container is the step's
  alone, so its peak is the step's peak.
- The host executor has no cgroup: "not measured", never a guess.
- Limit: where two steps share a container the numbers are the container's.
  `memory.peak` counts from the container's start, so a step that does not set
  a new high gets the highest reading taken each second, which can miss a
  spike shorter than a second. The record says which method gave the number.

## Requirements and how each is met

### 1. Isolation: a step's memory and CPU are its own

| Option | Isolation | Verdict |
|---|---|---|
| Keep one container per image per run | None. Steps share one limit. | Not acceptable. |
| One pod per step | Full. Each step has its own limit. | Meets it. |
| One pod per run, one container per step, all declared at the start | Full per step, but every step's reservation is held for the whole run. | Meets it, but wastes quota. |
| One pod per run, containers resized in place | Per container, but a container still holds one image's steps. | Meets it only with one container per step, and containers cannot be added to a running pod. |

Recommendation: one pod per step (stage 1 below). Isolation then comes from
the pod boundary, not from careful ordering.

How small is a quick fix in the current executor? Not small. A container is
named after its image, the pod is created once with a fixed set of containers,
and Kubernetes does not let a running pod gain a container. Separating
concurrent same-image steps means either more containers declared up front
(more reserved quota, the opposite of the goal) or a pod per step. The pod
per step is the real fix and is a stage of its own, not a patch. It is not
done before this design is reviewed.

### 2. Language-agnostic by construction

Measurement and enforcement are in the container and its cgroup. No code
knows about Go, Python, .NET, JVM, Node, Rust, C, C++ or shell. The Station
reads kernel counters and the kernel enforces limits. Anything language-aware
(requirement 3) is an optimization on top and may be absent without harm.

### 3. Tools told the truth about their limits

Many runtimes size themselves from the host: its CPU count and its total
memory. In a container with a limit that is wrong, and it is the commonest
cause of kills. Today the executor sets one number, N parallel jobs
(`internal/resources`, `ToolEnv`), as `GOMAXPROCS`, `MAKEFLAGS=-jN`,
`CARGO_BUILD_JOBS`, `CMAKE_BUILD_PARALLEL_LEVEL`, `UV_THREADPOOL_SIZE` and
`LAUNCHWAY_PARALLELISM`, and never replaces a variable the step set. N comes
from the CPU limit and from the memory per job. Nothing tells a runtime its
memory limit. The proposal is a fixed, documented set, derived from the step's own
limit, each one overridable by the step's `env:`:

| Tool | Hint | Derived from |
|---|---|---|
| Go | `GOMAXPROCS` (exists), `GOMEMLIMIT` (new) (about 90% of the memory limit) | the step's limits |
| Node | `NODE_OPTIONS=--max-old-space-size=N` (about 75% of the memory limit) | memory limit |
| JVM | `JAVA_TOOL_OPTIONS=-XX:MaxRAMPercentage=75 -XX:ActiveProcessorCount=N` | both |
| .NET | `DOTNET_GCHeapHardLimit` (about 75%), `DOTNET_PROCESSOR_COUNT` | both |
| Python | `OMP_NUM_THREADS`, `PYTEST_XDIST_AUTO_NUM_WORKERS`, `MALLOC_ARENA_MAX=2` | CPU limit |
| Rust, make, cmake | `CARGO_BUILD_JOBS`, `MAKEFLAGS=-jN`, `CMAKE_BUILD_PARALLEL_LEVEL` (all exist); ninja reads `MAKEFLAGS` or needs `-j` (new: a documented `LAUNCHWAY_PARALLELISM` use) | the thread count already computed (CPU, and memory per job) |

Rules: never override a value the step or image set; say in the step's log
which hints were set and from what; document the table in the pipeline
reference. The JVM, .NET and Go hints are safe defaults in modern versions
and ignored by old ones. Because these are only hints, a step that ignores
them is still held by the cgroup (requirement 2).

### 4. Always progressing or plainly stopped

| Part | How it is met |
|---|---|
| (a) Every wait has a reason, an estimate and a maximum | The reason exists (the queue sentence, now truthful). Add an estimate from the packing model below and a maximum, `queues.max_wait`, which already exists and ends a wait with a message. Make the estimate always visible, and make the maximum default on. |
| (b) A build that can never fit fails at once | Partly met: a single pod larger than the whole quota fails at once. Extend it to the run: if the largest step's figure plus the platform's reserve exceeds the ceiling, refuse the run when it is created, with the numbers. |
| (c) A stalled step is flagged and ended | New. A step with no output and under a small CPU use (measured by the same sampler) for a time `stall_after` (default 15 minutes, configurable per project) is flagged, then ended with what it was doing (its last log lines and its CPU and memory). Off for steps that declare `timeout` shorter. |
| (d) Retries are bounded and typed | Resource kill: one automatic retry with more, then stop. Infrastructure fault (lost Station, lost pod): at most 3 (`MaxLost`, exists). A step that failed on its own (exit code): zero unless the pipeline says `retry`. Never a loop. A wait for room is not an attempt but is bounded by `max_wait`. |
| (e) Self-deadlock is detected | New. If a run waits for quota and the pods that hold it belong to the same run (as in tonight's failure), the Station says so, releases its idle or abandoned pods and tries again once. The message names it. Per-step pods make this case rare. |

### 5. Finished work is never lost to a brief fault

Already true for a server outage: logs and results are spooled to the
Station's disk and delivered when the server answers (`internal/runner/spool.go`).
Added in this branch: a longer lease and independent heartbeats, so a slow
server does not cost a running step. To add: when a lease is lost after the
step finished, the Station keeps the result and offers it once, and the server
accepts a result for an attempt it has not yet re-run. Completed steps are
already kept by the server; a Station restart loses only the step running on it.
The stability suite (requirement 7) proves each of these.

### 6. The platform is protected from builds

| Protection | How |
|---|---|
| Ceiling | The step namespace's quota is the ceiling; the Station also keeps its own `max_cpu` and `max_memory`. Add a run-level ceiling per project. |
| Low priority | Build pods get a low `PriorityClass` and, where the cluster allows, a lower QoS than the platform's own pods, so the kernel and the scheduler sacrifice builds first. |
| Back-pressure | When the server or the database answers slowly, Stations slow their log and heartbeat rate and the server tells them to wait (HTTP 429 with a pause); the queue stops handing out steps. |
| Runaway builds | Per-step limits (requirement 1), a scratch-disk limit per pod (`ephemeral-storage`), and a maximum pod age (exists). |

### 7. Proof, not hope: a stability suite in the pipeline

Fault injection, each with the outcome the product must show:

| Fault | Expected outcome |
|---|---|
| Kill the Station in the middle of a step | The lease expires after 90 s, the step runs again once on another Station, the abandoned pod is gone, finished steps are kept. |
| Delay server answers by 20 to 30 s | The step keeps its lease and its result arrives. |
| Exhaust the quota | The next step waits with a truthful reason, an estimate and a maximum; it starts when room appears; no pod of its own run is among the holders. |
| Make a step use more memory than it has | It is killed, retried once with more, the new figure is recorded and the log says so. A second kill fails the step with a plain message. |
| Fill the scratch disk | The step fails with a message that names the disk, the pod is removed, nothing else is harmed. |
| Stall a step (no output, no CPU) | It is flagged, then ended with what it was doing. |
| Ask for more than the ceiling | The run is refused when created, with the numbers. |

Sample pipelines for Go, Python, .NET, JVM and Node, each with no resource
numbers written, must pass on the Kubernetes executor, run by the pipeline on
every change to the executor, the runner or the engine's queueing.

## Making reservation automatic

### Where to run steps (the choice)

| | One pod per step (i) | One pod per run, resize in place (ii) | One pod per run, small requests, limits from measurement (iii) |
|---|---|---|---|
| Start-up cost | A pod start per step: seconds, plus an image pull on a cold node. | One start. | One start. |
| Workspace and cache | Must be handed on: a volume per run, or the Station's checkout copied in, as a resume already does. Costs seconds for large trees. | Shared. | Shared. |
| Quota | Exact: each step reserves only its own, and gives it back when the pod ends. | Quota may count the new figure; see below. | Requests small, but a limit above the request is not guaranteed; the node can be overcommitted and kill under pressure. |
| Isolation | Complete. | One container per image only: same-image steps still share. | Same. |
| Failure modes | A pod that cannot start does not hold the others. A lost step loses only its own pod. | A resize that the node cannot grant stays pending (a defer state) with the step already running. A memory decrease is best effort. | Under node pressure, kills at random among overcommitted pods: the instability the owner does not want. |

What Kubernetes 1.35 says about in-place resize (from the Kubernetes
documentation, "Resize CPU and Memory Resources assigned to Pods"):

- It is stable since 1.35, and the feature gate can no longer be turned off.
- A container's `resources` are mutable for CPU and memory. By default a
  resize does not restart the container (`resizePolicy` `NotRequired`); the
  policy can be set to restart for memory.
- Lowering a memory limit is best effort: "the kubelet will make a best-effort
  attempt to prevent oom-kills ... but doesn't provide any guarantees"; if
  usage is above the new limit the resize waits.
- The pod's QoS class cannot change, and a pod's containers cannot be added.
- The scheduler uses the largest of the desired, allocated and actual request
  while a resize is open. A resize the node cannot grant is `Deferred` and is
  retried; one that can never fit is `Infeasible`.
- What a ResourceQuota does with a resize: the documentation pages for resize
  and for resource quotas do not say. This must be tested on a real 1.35
  cluster before anything depends on it. If a resize is not checked against the
  quota, a run could pass its ceiling; if it is checked and refused, a step
  would have to wait while already running. Both are failure modes worth a test
  of their own, so (ii) is not recommended as the first step.

Recommendation on the three: (i) one pod per step. It is the only one that
gives exact quota, complete isolation and no overcommit. Its cost is start-up
and workspace hand-over, which a per-run volume or the existing checkout copy
answers, and which the stage grouping below reduces. (ii) is a later
optimization for long steps, once quota behavior is tested. (iii) is rejected.

A middle form for short steps: one pod per stage when all of its steps share an
image and run one after another, so the cost of start-up is not paid for a
step of seconds. The boundary of isolation stays the pod; steps that run at the
same time never share one.

### The automatic figure

For each step, keyed by project, pipeline, step id and image, keep what the
measurement stores: the peak memory and CPU of each attempt.

| Question | Proposal |
|---|---|
| What is used | The highest peak of the last 10 successful attempts, not the average. A step that is killed does not count. |
| Headroom | 25% on memory, with a floor of 128Mi; on CPU, the request is the p90 of the peaks and no CPU limit is set, so a step is never slowed below what it could use. Memory is limit = request, so the kernel's behavior is predictable. |
| Following growth | A new higher peak raises the figure at the next run, at once. Falling is slow: the figure drops only when the highest peak of the last 10 has dropped, so a heavy step is not starved by one light run. |
| Minimum history | 3 successful attempts before the figure is trusted. Until then the first-run figure applies. |
| First run, no history | A class default for the kind of step, from the installation's defaults that already exist, with the pod ceiling. The first run is observed and its attempt becomes history. A first run that is killed is retried once with more (below). |
| Hand-written `resources:` | An override, as today, for the step it is written on. It is also a floor: Launchway never reserves less than a written figure. It is not a ceiling: if the step is killed, the one automatic retry still raises it, and the log says the figure written is too low. The reasons: a number a person wrote must be obeyed, and a number that is too low must not cause a loop. |

The zero-config rule: a pipeline with no numbers at all runs correctly,
including its first run, because the first-run figure is a safe class default,
the pod ceiling is checked, and the one retry covers a mistake.

### Recovery from an out-of-memory kill

1. The runner sees exit 137 or an `OOMKilled` status. The record already marks
   it as a kill (`memoryKillNote`).
2. The server retries the step once with the memory limit doubled, up to the
   ceiling; if it is already at the ceiling it fails at once with a message.
3. The new figure is recorded as the step's figure, so the next run starts
   there.
4. The log says it plainly: "This step was killed for memory at 2Gi. It runs
   again with 4Gi. The figure is kept for the next run."
5. A second kill fails the step. It is not retried again: no loop.

### Packing and the wait estimate

With figures per step, the queue can place a step by what it will really use.

- The ceiling is the namespace quota minus a platform reserve. The queue holds
  a running total of the reserved figures of the pods it knows.
- A step starts when its figure fits under the ceiling. Steps are taken by
  priority, then by age. A small step may pass a large one that waits only if
  that does not delay the large one past its estimate (no starvation: the large
  step has a reserved place after its estimate).
- The wait estimate is the time until enough reserved figures end. For each
  running step, the end is its usual duration from history (the estimate that
  exists already). The queue shows: "waiting for 3Gi; step A (about 2 min left)
  and step B (about 5 min left) hold it; about 2 min".
- A run that can never fit is refused when created (requirement 4b).

### What is shown to the user

- Per step: reserved, used (peak), and the figure for the next run.
- Per pipeline: where memory is wasted, as a list ordered by reserved minus used
  (for example "test reserves 4Gi and uses 1.1Gi: the next run will reserve
  1.5Gi"), and where steps are near their limit.
- On the queue: the estimate and the holders, as above.
- When a figure changed: one line in the step's log, naming the old and new.
- A recommendation page for a pipeline whose written `resources:` are far from
  what the steps use, with the measured figure beside each.

## Seeing what it costs: footprint and capacity

The owner's need: real, observed numbers, for capacity planning, for
installations with thousands of pipelines and hundreds of runs going all the
time, and numbers customers need to know. Everything here is measured by
Launchway itself and kept in its own database. No outside monitoring system is
needed (a product rule). Where a number is a forecast, it says so and shows how
wrong it may be.

An illustration, from a small installation, with no names: two idle servers
used 35 to 48 MiB each, an idle Station 14 to 44 MiB, the database 144 to
272 MiB, and one build pod reserved 8448 MiB while using 915 MiB at 1.75 CPU.
That last pair, reserved against used, is the first thing the page must show:
it was nine times too large, and nobody could see it.

### 1. The platform's own footprint

| What | How it is measured | Kept |
|---|---|---|
| Each server | Every server records, once a minute, its own resident memory, CPU time, goroutines, open connections, and requests per second (the Go runtime and the process's own cgroup). | One row per server per minute, rolled up (see 7). |
| The database | Connections in use and the pool's limit; database size and its growth per day, and the largest tables (one query a few times a day; PostgreSQL and SQLite both answer it). | One row per sample. |
| Each Station | At rest: its resident memory and CPU. While running N steps: the same, with N, so the cost of a step on the Station itself is known. Sent with its heartbeat, so it needs no new connection. | One row per Station per minute, rolled up. |
| Pods | The Station's own measurement of each step (stage 0), including pod start-up time. | `step_usage`. |

A Station's figures include only the Station process. The steps' pods are the
builds' cost and are counted per step.

### 2. Per step, pipeline, project and Base

For each level, over a chosen period: the typical value (median) and the peak
(the highest) of CPU and memory; reserved against used (the ratio and the
difference); wall time; time in the queue and the reason; cache hit rate; and
the trend of each (the same figure for the last 7, 30 and 90 days, and its
slope). "Per Base" is the sum over its projects. Everything is computed from
rows that already exist or that stage 0 stores:

| Needed | Source |
|---|---|
| CPU, memory peak, reserved | `step_usage` (migration 54). Add the container's `reserved_memory`/`reserved_cpu_milli` as stored; add the image and the executor name. |
| Wall time, queue time | The step events (queued, leased, started, finished) which the engine already keeps; copy the two durations into the usage row so a rollup needs no event reads. |
| Queue reason | `waiting_code` already on the step; keep its total waiting time per code. |
| Cache hit rate | The cache results already in the step's finish event; copy hit or miss into the row. |

### 3. Capacity

| Question | Answer shown |
|---|---|
| How much was used? | Station-hours and memory-hours per day (the sum over steps of wall time, and of wall time times peak memory), and reserved memory-hours beside them. |
| How busy? | Concurrency over time: running steps and running pods per minute, as a line, with its highest value per day. |
| How full was each ceiling? | For each ceiling (the namespace quota, a Station's concurrency, the run-level ceiling): the share used, per minute, and the time spent at 90% or more. |
| Why did builds queue? | Queue time per day split by reason (quota full, Stations busy, no Station, retry pause) and the number of steps affected. |

### 4. Forecast

The page has one question: "I expect N runs a day of this mix. What do I
need?" The answer is derived from history only:

1. Take the last 30 days of runs and group them by pipeline. Each pipeline's
   mix share and its per-run cost come from its own rows: Station-seconds,
   memory-seconds, and the highest concurrent reserved memory of one run.
2. Scale to N runs a day by the mix. Compute the busiest hour from the
   observed daily shape (the ratio of the busiest hour to the daily average
   seen in the history).
3. The Stations needed are the busiest hour's concurrent steps divided by what
   one Station of the chosen size runs at once; the memory is the busiest
   hour's concurrent reserved figures plus the platform's reserve. The server
   and database figures use the observed cost per request and per run from
   section 1, times the same scale.
4. The error bar comes from the history, not from a guess: repeat the
   calculation on each of the last weeks (a rolling estimate) and show the
   spread (the 10th to 90th percentile of the error against what was actually
   needed). With fewer than 14 days or fewer than 100 runs, the page does not
   forecast; it says how much history is missing.
5. A forecast never extrapolates a trend beyond the observed range of run
   counts by more than a factor of 3; beyond it the page says "outside what
   has been observed" and shows only the linear scaling, labeled as such.

### 5. Waste findings

Each finding states the rule and the evidence, and names the step:

| Finding | Rule |
|---|---|
| Reserves far more than it uses | Peak memory under 50% of reserved on at least 10 runs; show the figure the next run would get and the memory-hours it would give back. |
| Could run in parallel but does not | Two steps with no `needs` path between them that ran one after the other, with the time saved if they had run together and whether the ceiling would have allowed it. |
| Cache never hits | A cache with a hit rate under 5% over at least 20 runs, with the Station-seconds and bytes it costs to save it. |
| Near its limit | Peak above 90% of the limit on several runs: a kill is likely; the next figure is higher. |
| Idle waits | A step that waits in the queue for longer than it runs, repeatedly. |

### 6. Where it is shown, and who may see it

- A **Capacity** page: footprint, per-pipeline table, capacity lines, forecast,
  findings.
- A line on each run (its total reserved against used, memory-hours) and on
  each step (already built: "reserved 4Gi, used 1.1Gi peak").
- The same data in the API (`/api/v1/capacity/...`), the CLI
  (`launchway capacity`), the MCP server, and as metrics in an open text
  format (the plain `name{labels} value` exposition) on an endpoint that any
  collector may read. Parity: each is generated from the same query, so none
  can show a figure the others do not.
- Who sees what: a Base sees only its own projects, runs and Stations, and its
  share of the shared servers and database as a labeled estimate. An
  installation administrator sees every Base, the servers, the database and
  every Station. Metrics for an administrator are labeled by Base; a Base's
  metrics have no label that names another Base. No step's script or
  environment is in any of this, only numbers and names.

### 7. Retention and the cost of keeping it

A raw row is one step attempt, about 150 bytes plus index (about 250 bytes).

| Runs a day | Steps per run | Raw rows a day | Raw kept 30 days | Size |
|---|---|---|---|---|
| 1,000 | 10 | 10,000 | 300,000 | about 75 MB |
| 100,000 | 10 | 1,000,000 | 30,000,000 | about 7.5 GB |

At 100,000 runs a day, raw rows beyond a few days are too many to keep long,
so the design rolls up:

- Raw rows are kept for 7 days by default (configurable), then removed.
- An hourly rollup per (Base, project, pipeline, step, image) keeps counts,
  sums, the maximum, and a small fixed histogram of memory and CPU (so the
  median and the 90th percentile can be had without the raw rows). A step that
  runs every hour gives 24 rows a day; 10,000 distinct steps give 240,000
  rows a day at most, whatever the number of runs. These are kept 90 days.
- A daily rollup of the same keeps 2 years: at most 10,000 rows a day.
- Footprint samples: one row a minute per server, Station and database is 1,440
  rows a day each; a 200-Station installation adds 288,000 rows a day, rolled to
  hourly after 7 days.
- The rollup runs in the server's existing background loop, in small batches,
  so it cannot hold the write lock. Deleting raw rows uses the same batches.
- What is lost by rolling up: exact peaks of one attempt after 7 days (the
  maximum of the hour is kept), and per-attempt detail. The percentile error of
  the histogram is stated on the page (its bucket width).

What the schema needs, to be designed in stage 5 (not built now): a rollup
table with the keys above, a footprint table, and columns in `step_usage` for
wall time, queue time, image and cache result. All are additive.

## The platform scales by itself and builds can never take from it

Facts today: the chart already runs step pods in their own namespace
(`runner.kubernetes.namespace`) with its own quota, and has
PodDisruptionBudgets for the servers and the Stations. It has no autoscaling
for either: no HorizontalPodAutoscaler template and nothing that scales
Stations by queue.

### 1. Servers

| Question | Proposal |
|---|---|
| What drives it | An optional HorizontalPodAutoscaler in the chart (off by default until measured, on by default after stage 5b). First signal: CPU and memory, which any cluster has. Better signal: the server's own request rate or latency, exported in the open text metrics format, read through the cluster's custom-metrics path where one exists. |
| Bounds | `minReplicas` 2 (so one can be replaced without an outage), `maxReplicas` set by the installer from the forecast, never above what the database's connection limit allows (each server holds a pool; the installer divides the limit). |
| Scale up safely | A new server is ready only after its migration check, its database connection and its first successful read. Sessions are in the database, so any server answers any request. Leases are in the database, so any server accepts a heartbeat. |
| Scale down safely | Remove one server at a time, with a stabilization window of several minutes. A server being removed stops taking new requests (readiness fails), finishes the requests it has, and ends streaming connections (live log and event streams) with a normal close that the browser and the CLI reopen on another server. A long upload in progress is given the termination grace period; a Station's spooled upload resumes on another server by its log position. No lease or session lives only in a server's memory, so removal loses nothing. |
| What must be proven | A test that scales servers up and down under load, with Stations heartbeating and a browser following a log, loses no step, no log line and no session. |

### 2. Stations: scale by queue, not CPU

A Station's CPU says little: it mostly waits for its steps. What matters is
the number of steps waiting and what they need. The server knows both: the
waiting steps, the figure each needs (the measured one), and what each running
Station has free.

Two ways to act on it:

| | Built in: the server asks for replicas | The cluster's own autoscaler reads a "wanted Stations" figure |
|---|---|---|
| How | The server computes the wanted count and sets the Station Deployment's scale through a narrow Role (only `scale` on that one Deployment). | The server exports `launchway_stations_wanted` as a metric. An autoscaler (an HPA on an external metric, or an event-driven scaler) reads it and scales. |
| For | Works with nothing else installed. Same behavior everywhere there is a Kubernetes API. One place to test. | Uses what the cluster already has; the customer's own policy and tools apply; the server holds no write right on workloads. |
| Against | The server holds a write right on a workload, small but real; two controllers must not fight if the customer also adds an autoscaler. | Needs the autoscaler and a metrics path to be installed; behavior differs by cluster. |
| Zero-config | Yes. | No: it needs something installed. |

Recommendation: export the figure always (it is also what the Capacity page
shows), and build the narrow-Role scaler in as the default, switched off if
the customer sets `stations.autoscaler: external`. Then both ways exist and
neither fights: when external is set, the server only exports.

Rules for either:

- Up: when steps have waited for a Station for longer than a short time (30
  seconds), add Stations to cover the waiting steps' measured needs, up to the
  ceiling `stations.max`. Never above what the build namespace's quota could
  hold, because a Station that cannot start its pods is waste.
- Down: remove a Station only when it has been idle for a set time (10
  minutes), one at a time, and never one that holds a running step. A Station
  that is chosen for removal first stops taking steps (it tells the server it
  is draining), finishes what it has, delivers its results, and then exits.
  The Deployment's own downscale picks an arbitrary pod, so the built-in
  scaler must pick the idle one: it deletes that pod with a cost annotation
  (the cluster prefers to remove the cheapest pod) or sets the Station to
  drain and lets it exit by itself. This must be tested on a real cluster.
- Floor: `stations.min` (default 1), so a step never waits for a cold start
  when the platform is quiet.

### 3. Separation is the default, not an option

Three tiers, each with its own namespace, its own quota and its own priority:

| Tier | Holds | Namespace | Priority | Quota |
|---|---|---|---|---|
| Platform | Servers, database | `launchway` | highest (a class created by the chart) | A ceiling that leaves room for the autoscaler's maximum. |
| Stations | Station pods | `launchway-stations` | middle | A ceiling at `stations.max` times a Station's size. |
| Builds | Step pods | `launchway-steps` | lowest, and preemptible | The build ceiling; the owner's main knob. |

The installer creates all three, with the quotas and the priority classes. A
chart value can collapse them for a small trial, but the default is three, and
the installer says plainly when it is told not to.

What each protection really gives (these are different things, and the
document must not blur them):

| Mechanism | What it guarantees | What it does not |
|---|---|---|
| Namespace ResourceQuota | A ceiling: builds in one namespace cannot ask for more than it allows. It stops builds from taking more than their share of requests and limits. | It reserves nothing. Free capacity of the node is not held for the platform. |
| LimitRange | Defaults and bounds for each pod, so a pod with nothing written is not unbounded. | Same: not a reservation. |
| Requests | What the scheduler sets aside on a node: a pod with a request is never placed where the request does not fit. This is the platform's real reservation, but only while the scheduler has to find room for it. | A request of memory does not stop other pods from using memory that is free; it only counts at scheduling and at eviction ranking. |
| Priority class | Under pressure, the scheduler can preempt lower-priority pods to place a higher one, and the kubelet evicts lower-priority pods first. | It does not help if all pods are the same priority. |
| Limits | The kernel stops a pod at its memory limit (a kill) and throttles CPU at its limit. This is what protects a node from one pod. | A limit above the node's real memory protects nothing. |
| Separate nodes (labels, taints) | The only guarantee that a build cannot run on the platform's node at all. | Costs spare capacity: nodes are not shared. |

So on one node, only requests, priority and limits protect the platform, and a
quota alone does not. On a shared node, the design relies on: the platform's
requests set deliberately, the highest priority, and build pods with limits
and the lowest priority. With separate nodes, add a taint on the build nodes
that only build pods tolerate. The installer asks whether separate nodes exist
and says which guarantee it can and cannot give.

Default requests and limits for the platform, set deliberately:

| Pod | Class | Why |
|---|---|---|
| Server | Guaranteed for memory (request equals limit), CPU request set, no CPU limit. | A server must not be killed for memory by chance, and it must not be throttled below what it needs. Guaranteed memory gives the lowest eviction rank. |
| Database | Guaranteed (request equals limit for both). | The most costly thing to lose. |
| Station | Burstable: memory request set from the measured idle and N-step footprint, limit about twice it; low CPU request. | A Station's memory grows with the steps it supervises; a limit at twice the measured figure is room, not waste. Priority protects it from build pods. |
| Build pod | Memory request equals limit (from the automatic figure); no CPU limit, CPU request from measurement; lowest priority. | Predictable kills and no throttling; first to be preempted. |

The numbers are not guessed: they come from the measured footprint (the
illustration above: idle servers at 35 to 48 MiB, an idle Station at 14 to
44 MiB, the database at 144 to 272 MiB), plus headroom, and the installer shows
the figures it used. Until an installation has history, the chart's defaults
are conservative and documented.

### 4. "What should we ask for": the installer and `doctor capacity`

- The installer, given the cluster's size and the expected load (a number of
  Sorties a day, or a choice of small, medium, large), proposes: the three
  quotas, the server's and Stations' bounds, and the database's size and
  connection limit. It shows the arithmetic and writes the values into the
  chart's values, which the customer can change.
- `launchway doctor capacity` reads the installation's own history (section on
  footprint) and its cluster's quotas and says:
  - the recommended ceilings and bounds, from the measured numbers, with the
    forecast's error bar;
  - where a ceiling is too low: **a ceiling that can never fit one build of a
    known pipeline** (the largest concurrent reserved figure of any pipeline
    run in the last 30 days, plus the platform reserve, against the build
    namespace's quota) is an error, named by pipeline and number;
  - where a ceiling is too high for the node or cluster (a quota larger than
    the nodes' allocatable memory);
  - where an autoscaler's maximum cannot be satisfied by the quota or the
    database's connection limit.
  It exits non-zero on an error, so a pipeline can run it.
- The same check runs when a pipeline is added or changed, as a warning.

### 5. When capacity is short

| Situation | What happens |
|---|---|
| Build ceiling reached | Steps wait. Each wait has a reason, an estimate and a maximum (requirement 4). Builds are slower, never broken. |
| Stations at their maximum | Steps wait with the reason "all Stations busy", with the estimate. The scaler does not exceed the ceiling. |
| The server or database is slow | The platform sheds build intake before it degrades: the queue stops handing steps to Stations (a short, visible pause with the reason), Stations slow their log and heartbeat rates when told, and new Sorties are accepted but held. The web and the API keep answering. |
| Node pressure | Build pods are preempted or evicted first (lowest priority, limits equal requests so their kills are predictable). Platform pods are not evicted by build pods: their priority is higher and their memory is Guaranteed. The step is retried as an infrastructure fault, within its bound, and the log says so. |
| Nothing is ever taken from the platform tier | This is the tie-breaker: if a trade-off must be made, the build waits, is slowed or is retried; the platform keeps running. |

### 6. Clusters without an autoscaler, and installs that are not Kubernetes

| Place | What the same ideas mean |
|---|---|
| Kubernetes without a pod autoscaler | The chart's replica counts are fixed numbers set by the installer from the forecast. The server still exports "wanted Stations" and the Capacity page shows when the fixed count was too low. The narrow-Role scaler still works (it needs only the Deployment's scale), so Stations can scale without any autoscaler installed; the servers stay fixed. |
| Kubernetes without a node autoscaler | The ceiling is what the nodes hold. The installer checks the quotas against the nodes' allocatable figures and warns when the sum of the three tiers' ceilings exceeds them. |
| One machine | Three tiers become three systemd slices (or three container groups) with CPU and memory limits, and build containers get the lowest weight and a hard memory limit; the platform slice has a memory guarantee (`MemoryMin`) so the kernel reclaims from builds first. The Station's concurrency is the build ceiling. Scaling is the Station's concurrency number; the Capacity page says when to add a machine. |
| Several virtual machines | The platform runs on its own machines, Stations on others. Scaling is adding a Station machine with the join token; the server's "wanted Stations" figure says how many are wanted, and a hook (a command or an API call that the customer supplies) can create them. The server never creates machines itself. |

Stage in the plan: the scaler, the three tiers in the chart and the installer,
and `doctor capacity` form stage 6 (protection), together with the stability
suite's fault tests for scaling down a server mid-stream and removing a
Station mid-step. Estimated 3 to 4 weeks more than the original stage 6.

## Recommendation and staged plan

Recommendation: finish isolation first, because it is the cause of the latest
failures and no figure can be trusted while steps share a limit. Then use the
measurement already built to compute the figures, then add recovery, then
packing.

| Stage | What | Size (honest) |
|---|---|---|
| 0 (done in this branch) | Release on lost lease, restart sweep, heartbeat tolerance, truthful message, measurement and its display. | Done. |
| 1 (done: see "Stage 1 as built") | One pod per step on Kubernetes: pod naming by step, workspace hand-over (reuse the checkout copy; add a per-run volume as an option), reaper changes, quota checks per step, tests with the fake client and one real-cluster rehearsal. Retires the per-image sizing in `runContainers`. | 2 to 3 weeks. The workspace hand-over and the real-cluster testing are most of it. |
| 2 | Automatic figure: store history, compute figures, apply them when the run is created, first-run defaults, override as floor, display reserved, used and next figure. | 1 to 2 weeks. |
| 3 | Kill recovery: one typed retry with more, the record and the plain message; the retry typing for infrastructure faults. | About 1 week. |
| 4 | Environment hints for tools (requirement 3) with a documented table and tests per language image. | About 1 week; the table is easy, proving it on real images is the work. |
| 5 | Packing, estimate with holders, run refusal when it can never fit, self-deadlock detection, stall detection. | 2 weeks. |
| 5b | Capacity page: footprint samples, rollups, per-pipeline figures, waste findings (needs only stage 0 data and the rollups). The forecast comes last, after 14 days of history exist. | 3 to 4 weeks, forecast included. |
| 6 | Protection and scaling: priority class, back-pressure, scratch disk limit, run-level ceiling; three tiers in the chart and installer; server autoscaler; Station scaler by queue; `doctor capacity`. | 4 to 6 weeks. |
| 7 | Stability suite and the five sample pipelines, in the pipeline. | 2 weeks, starting with the first fault tests in stage 1 and growing. |
| Later | In-place resize for long steps, only after a test on a 1.35 cluster shows what the quota does. | Unknown until tested. |

Total: about 16 to 21 weeks of focused work, in the order above. Stages 1
and 7 should begin together, so each stage is proved when it lands.

## Stage 1 as built

Stage 1 is built: the Kubernetes executor makes one pod for each step attempt
(`kubernetes.pod: per-step`, the default). The earlier behavior stays for one
release as `kubernetes.pod: per-run` (chart: `runner.kubernetes.pod`), so an
installation can go back without a downgrade; it will be removed. The local,
Podman, Docker and host executors are unchanged.

### What was built

- **A pod for each step attempt.** The pod holds the step's own container (its
  image and its own requests and limits), the helper container that holds the
  workspace, and the run's service containers, and nothing else. It is made when
  the step starts and deleted when it ends, however it ends (success, failure,
  cancel, timeout, lost lease). Its name is
  `launchway-<step, 20 characters>-a<attempt>-<10 hex of a hash of run, step,
  attempt, lease and Station>`: a valid DNS label, different for every attempt.
  Labels carry the run, the Station and `launchway.dev/pod=step`; annotations the
  step and attempt.
- **The step's own container.** `runContainers` still sizes one container per
  image for the largest step (older Stations and `per-run` use it). The server
  now also sends `Job.StepContainer`, the step's container sized for that step
  alone (`sizeContainer` with only that step), and a per-step Station uses it for
  the step's container. Acceptance case 1 and 4 follow: two same-image steps get
  two pods, each with its own memory; a run of any number of images never makes a
  pod of more than the step's container, the helper and the services.
- **The workspace between steps: through the Station.** The Station keeps a copy
  of each running run's workspace on its own disk (`kubernetes.work_dir`, by
  default `kubernetes` under its data directory), first made from the checkout,
  which is never written to (for a run from a directory it is the person's own
  files).
  - A step's pod is filled from the copy when it starts (a pod starts empty, so
    what it lacks is the workspace), except for the paths its caches own and the
    paths the pipeline lists in `workspace: { exclude: [...] }`.
  - **Caches never travel with the workspace.** The runner tells the executor
    which paths the step's caches own (`Spec.Cache`) and which of them are saved
    after the step. A cache is restored (the runner's `Place`) into a directory of
    the step's own on the Station, sent into the pod, and, if it is saved, what
    the step changed in it is read back into that directory, from which the cache
    feature stages and saves it. The directory is dropped when the cache is saved
    (or an hour after the step). The run's workspace copy never holds them.
  - **Only what changed comes back.** The Station remembers what it sent (size,
    time, mode, link target, from the tar headers: no file is hashed or read
    twice). The pod's clock is read when the pod is filled; when the step ends the
    pod lists every file with its status-change time (ctime, which `touch -d`
    and tools that restore times from an archive cannot set backwards) and its
    size and modification time. A file whose ctime is after the clock reading is
    changed; one in the same second is compared with what was sent (a whole second
    is the clock's grain); new directories are noted, names that were sent and are
    not listed are removed. Only the changed files are tarred, by name. Entries
    that arrive as they were sent are not written, so a step that ran at the same
    time cannot be undone by this one.
  - Reports, Cargo and image build contexts are read from the copy after the
    step (and fetched Cargo is put into it before the step), through the same
    local code the other executors use, so a failed step's reports are still
    there after its pod is gone. The runner builds an image's context from the
    copy (a test holds that an earlier step's changes are in it, the ignore file
    is applied, and the size limit is measured on what is kept).
  - Names are checked against escaping the copy (`os.Root`), links are kept as
    links and never followed, and devices, pipes and sockets are not copied.
  - **The Station's disk is bounded and visible.** `kubernetes.max_workspace`
    (4Gi) for one run's copy and `kubernetes.max_workspaces` (12Gi) for all copies
    and caches in transit; past either, the step fails with a plain message that
    names the setting; each step's log says what the Station holds. Copies are
    removed when the run ends, caches when they are saved. There is no Stations
    page figure for it yet.
  - **`workspace: { exclude }`** is the only other thing that changes what
    travels: trees that later steps re-create or get from a cache (a dependency
    directory) are left out by the pipeline, never by name in the executor.
- **Why through the Station.** It is what the first step's workspace always did
  (the checkout is copied into the pod), it needs nothing from the cluster
  (no storage class, no claim, no extra RBAC, no node affinity), and it makes
  every artifact, cache and report path the same as on the other executors. The
  per-run volume is kept as a documented option, not built: one claim mounted by
  each step's pod in turn saves the transfer, but it needs a storage class and
  permission to make claims, ReadWriteOnce confines the steps of a stage that run
  at the same time to one node (so parallel steps would wait for each other's pod
  or need ReadWriteMany), quota would also count the claim, and the volume must be
  cleaned when a Station dies. It is worth building when workspaces of gigabytes
  make the transfer the cost; until then `per-run` is the escape for such a run.
- **Parallel steps.** Today they share one filesystem live. Now each starts from
  the workspace as it was and hands back what it changed; they no longer see each
  other's files while running. A step that needs another's files must `needs:` it,
  as it always had to.
- **Quota and waiting.** The checks (operator limits, LimitRanges, quota) are made
  for the step's pod. A pod larger than the whole quota fails at once with both
  numbers (requirement 4b, per step). When the pod only does not fit now, the
  API server's own answer decides (the quota's status can be behind pods that
  were just deleted) and the step waits with the sentence that names the steps of
  the same run that hold room, what the namespace allows, what is reserved in all
  and how much must be released.
- **A run cannot wait on its own abandoned pods (requirement 4e, finding 3).**
  Before a step makes its pod, the Station deletes its own pods of that run that
  no step of its process runs in, and says so in the step's log. The reaper does
  the same for a restarted Station (pods it made in an earlier process hold no
  lease) and for a lost lease (`ReleaseRun`). Pods of another Station of the same
  run are running steps and are waited for; they end by themselves.
- **A restart of a Station (finding 5).** A restarted Station does not adopt the
  step that was running: its lease is gone and the step runs again once, on a
  lease of its own, while the steps that finished are kept. What changed is that
  the abandoned pod is removed at once, so the retry never waits for its quota.
  Adoption (reattaching to the running step) is stage 3, with the retry typing.

### What changed for the parts around it

| Part | Now |
|---|---|
| Image steps | They have no pod. The build context is read from the Station's copy of the workspace, which holds what the steps before left; the log says so. The `ExportContext` of a pod is used only with `per-run`. |
| The usage sampler | It reads the cgroup of the step's own container in the step's own pod, so the figures are the step's alone (the earlier limitation "where two steps share a container the numbers are the container's" is gone on Kubernetes). |
| Out-of-memory explanation | Looked at before the pod is deleted and kept for the runner, which asks after the step. |
| Tool caches under HOME | They live in the pod and go with it: a later step starts with the Go build cache and the like cold (before, steps of a run shared them). Declare `cache:` for what should carry on. |
| Services | They are part of every step's pod and start with it. A service's state no longer carries on to the next step, and each step pod counts the services against quota. This is a change for pipelines that relied on it (use `per-run` meanwhile). |
| Inspection steps | Each is a step with its own pod, so an Inspection of many steps makes many short pods, not one pod of 14 containers. |
| Private home, priority class, image pull secrets, restricted security context, user entry, readiness of services, the "waiting for the pod" notices | The same pod specification is used, so they are as before in each step pod. |
| Report and artifact collection, cancel, timeouts, the "little progress" notice | Unchanged for the runner. A canceled step's workspace is read back within 30 seconds, so its reports are collected; a step that ended by itself gets 10 minutes. |

### What it costs, measured

Measured on a one-node kind cluster, images already on the node, for a step
that does nothing, with a workspace of 2000 files of 10 KiB (20 MB), three steps in a row:

| Part | Measured |
|---|---|
| Whole step (pod start, user-entry init container, hand-in, hand-out, delete) | 3.4 to 4.2 s |
| Workspace into the pod | 0.14 to 0.51 s |
| Workspace back to the Station | 0.07 to 0.21 s |

A unit benchmark with the fake cluster (`BenchmarkStepWorkspaceHandOver`; tar runs
locally, no network, so it is the Station's own work) gives, per step, for the
hand-in, a no-op step and the hand-back: 19 ms for 100 files (400 KB), 149 ms for
2000 files (20 MB), 595 ms for 20,000 files of 4 KiB (80 MB) and 154 ms for 100
files of 1 MiB (100 MB). A real network adds the transfer through the API server.
So for steps of a minute or more the cost is not noticeable; a run of many tiny
steps is slower (the design accepted it: slower builds over an unstable platform),
and workspaces of gigabytes pay on every step (use `per-run`, or the per-run
volume, when that is built).

### A workspace of gigabytes, measured

The first version of the hand-over sent the whole workspace into every pod and
read the whole workspace back, which is wrong for pipelines whose steps keep tool
caches inside the workspace (this repository's pipeline does: `.cache/go-mod`,
`.cache/go-build`, `.cache/npm`, and `node_modules` in two places). The
measurement below is of a synthetic workspace made from nothing real: about 3 GB in
about 150,000 files (Go module cache 1 GB in 40,000 files and build cache 1 GB in
20,000 files declared as one cache, an npm cache of 300 MB in 10,000 files declared
as another, two `node_modules` of 320 MB and 240 MB in 70,000 files, source of 32 MB in
8,000 files). It runs the shape of this repository's own pipeline through the fake
cluster: its nine script steps in order (`docs`, `check-code`, `test`,
`security-go`, `security-web`, `build-ui`, `build-binary`, `gate-pipeline`,
`image-context`; the other nine of its 18 steps are approvals, a deploy, verifies,
rollbacks and the image build, which have no pod), each restoring the caches it
declares, touching the directories it touches, and saving them. The fake pod's
tar runs on the same machine and nothing crosses a network, so the figures are the
work of the Station and of tar, and the bytes are exact; a real cluster adds the
transfer through the API server. Test: `TestBigWorkspaceHandOver`
(`LAUNCHWAY_BIG_DIR`, `LAUNCHWAY_BIG_NAME`, `LAUNCHWAY_BIG_EXCLUDE=1`).

| | Before (whole workspace both ways) | After: caches kept out of the workspace, only changes come back | After, and the pipeline leaves the two `node_modules` out |
|---|---|---|---|
| Time in the executor, nine steps (pod fill, step, hand back) | 319 s | 147 s | 71 s |
| Longest single step | 72 s (`build-ui`) | 32 s (`security-web`) | 21 s (`build-binary`) |
| Bytes into pods, nine steps | 21.5 GiB | 11.9 GiB | 8.7 GiB |
| Bytes back from pods, nine steps | 22.2 GiB | 0.66 GiB | 0.10 GiB |
| Cache restore (store to the Station's directory), nine steps | 16.9 s | 12.9 s | 21.4 s |
| Cache staging for the saves | 59.9 s | 63.3 s | 29.1 s |
| Station's peak disk for workspace copies and caches | 2.79 GiB | 2.51 GiB | 1.99 GiB |
| Station process: Go heap peak, peak resident memory | 115 MiB, 138 MiB | 147 MiB, 168 MiB | 83 MiB, 108 MiB |

The restore and staging rows are the Station's local disk and vary by half between runs on this laptop; they are the cache feature's own work and are not changed by the hand-over (the right column's restore ran while the disk was busier). The memory row is the whole test process.

Per step, in the middle column: a cache step sends its caches into the pod
(2.2 GiB for the Go ones) because a cache is restored into the step's own pod, as
the cache feature always did on a cold workspace; that is the restore, not the
hand-over, and it is the part that remains. What comes back from a step is what it
changed: 0 to 3 MiB for the steps that only touch caches, 95 MiB for the one that
builds a 100 MB binary, 249 and 332 MiB for the two that install a `node_modules`.
The steps that do nothing (`gate-pipeline`, `image-context`) move 0 bytes back and
about 0.7 GiB (the workspace, `node_modules` and the binary) into their pods.

What is left is the workspace itself going into each pod, since a pod starts empty,
and the saved caches being staged. `workspace: { exclude }` removes a tree from
that for pipelines that can say which trees later steps do not need (right
column: the 560 MB of `node_modules` no longer goes into every pod that follows the step that made it). Nothing is left
out by name: a pipeline that keeps its dependency directories in the workspace and
does not exclude them pays for them in each step's pod, and that is its choice.
The Station keeps a hash of nothing: it reads each file once when sending it, and
the comparison of what came back is by size and time, from the headers.

### Acceptance cases and the tests that hold them

| Case | Test (`internal/executor` unless noted) |
|---|---|
| 1. Two same-image steps killed together | `TestTwoStepsOfOneImageAtTheSameTimeHavePodsAndLimitsOfTheirOwn`; `internal/engine`: `TestEachStepIsToldItsOwnContainerNotTheLargestOfItsImage` |
| 2. 16.9 GiB reserved while using 1 GiB | Quota is held only while a step runs: `TestAStepPodHoldsOnlyItsOwnContainerTheHelperAndTheServices`, `TestARetriedStepNeverWaitsForRoomItsOwnAbandonedPodHolds`, `TestAStepWaitsForRoomHeldByOthersAndSaysWhoHoldsIt` |
| 3. Retry waiting on its own abandoned pod | `TestARetriedStepNeverWaitsForRoomItsOwnAbandonedPodHolds` (a retry on a restarted Station, 9 of 16 Gi held), and on a real cluster `TestKubernetesStepQuota` |
| 4. Fourteen containers | `TestAStepPodHoldsOnlyItsOwnContainerTheHelperAndTheServices` |
| 5. Restart of Stations | `TestARestartedStationSweepsThePodsOfAttemptsThatHoldNoLease`, `TestReleaseRunDeletesTheIdlePodsOfTheRunAndNotARunningStep` |
| Pods gone after every outcome | `TestTheStepPodIsGoneAfterEveryOutcome` (success, failure, memory kill, cancel, image that cannot be pulled) |
| Workspace hand-over | `TestTheWorkspaceGoesFromStepToStep`, `TestStepsAtTheSameTimeMergeAndNeitherUndoesTheOther`, `TestFilesAreReadFromAndPutInTheRunsWorkspaceWithoutAPod`, `TestHandingBackNeverWritesOutsideTheWorkspace` |
| Sampler | `TestTheUsageIsReadFromTheStepsOwnPodAndContainer` |
| A step that can never fit | `TestAStepThatCanNeverFitTheNamespaceFailsAtOnce` |

The same behavior was rehearsed on a real cluster (a kind cluster, in a
namespace made for the rehearsal and deleted after): `TestKubernetesStepPods`,
`TestKubernetesStepOOM`, `TestKubernetesStepPodService`, `TestKubernetesStepQuota`
(`LAUNCHWAY_TEST_KUBE_CONTEXT`), and the cache end-to-end test on Kubernetes.

### Not done in stage 1

Adoption of a running step by a restarted Station; the automatic figure (stage
2); a per-run volume; reading only changed files back (the whole workspace is
read, and only what changed is written on the Station); a refusal of a run whose
largest step can never fit when the run is created (stage 5); the image-step
test on Kubernetes (`internal/api/images_kubernetes_test.go`) needs BuildKit and
was not run, though its wording was updated.

## Open questions for the owner

1. Is a pod per step acceptable for start-up time on the home cluster, given
   that images are already on the node after the first pull?
2. Should a written `resources:` be a floor (proposed) or a strict override?
3. Is 15 minutes the right default for a stalled step, given that some steps
   (a download, a long link) are quiet for a long time?
4. May build pods have a low priority class on the target cluster, and does
   the cluster have a metrics-free way to enforce a scratch disk limit?
