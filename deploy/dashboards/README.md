# Launchway dashboards and alert rules

These files are for people who connect their own monitoring. Nothing in Launchway depends on them: the
product has its own Insights pages for every Flight, every Squadron and the installation, and needs no
outside server. Use these when you already run a metrics store and a dashboard server and want
Launchway's health next to everything else.

They are built only on the metrics Launchway serves (see `docs/reference/metrics.md`; the page is
generated from the code, and a test fails if a dashboard or a rule names a metric that does not exist).

## What is here

| File | What it shows |
|---|---|
| `platform-health.json` | Requests, errors and latency by route template; the database connections and statement time; schema version; background workers; sign-ins; memory |
| `pipelines-and-queues.json` | Queue depth and wait by pool; Stations and leases; runs and steps by outcome and time; cache hits; approvals waiting; deploys and rollbacks |
| `git-hosting.json` | Git operations, time and bytes; mirrors and their lag; webhooks in and out; free space on the volume holding repositories |
| `alerts/*.rules.yaml` | The same three as alert-rule examples: queue stuck, Stations lost, error rate, worker stalled, repository disk low, and more |

The dashboards are JSON for Grafana (schema version 39) and use a data source of the Prometheus type, chosen in
a `Data source` variable at the top. A `Job` variable limits them to the scrape jobs that serve Launchway.
The rules are Prometheus alerting rules. They are examples: change the thresholds to what your installation needs.

## Use them

1. Make your metrics store scrape Launchway. With the Helm chart: set `metrics.serviceMonitor.enabled` and
   `metrics.podMonitor.enabled` (they need the Prometheus Operator's API), and let your monitoring namespace
   reach the metrics port with `networkPolicy.metrics.from`. Without the chart: scrape `/metrics` on the
   address in `observability.metrics.listen` of each server, and on `--metrics-listen` of each Station. See
   [Metrics and traces](../../docs/howto/metrics-and-traces.md).
2. Import each `*.json` file: in Grafana, Dashboards, New, Import, then upload the file.
3. Load each `alerts/*.rules.yaml` as a rule file of your metrics store, or paste the rules into your alerting
   system.

## What the metrics never carry

No repository, branch, person or run id is ever a label, and no Flight or Squadron name unless
`observability.metrics.name_labels` is on (one extra metric, limited to 300 series). Metrics are for the
installation as a whole and for its operators. Per-Flight and per-Squadron views belong on the Insights pages,
which obey permissions.
