# Metrics and traces

Launchway tells the outside how it is doing in three ways. None of them needs an outside server.

- **Insights pages** (Activity, Insights; a Flight's or a Squadron's Insights tab): charts of runs, steps, tests and delivery, worked out from Launchway's own database. They obey permissions: a person sees only the Flights they may read.
- **Metrics**: counts, gauges and histograms in the text exposition format, for the monitoring system you already run. They are for operators and cover the whole installation.
- **Traces**: one trace per run and per deploy, sent over OTLP/HTTP to a collector. Off until you set an address.

## See the Insights pages

Open **Activity**, then **Insights**, for every Flight you may read; a Flight's page has an **Insights** tab; a
Squadron's page has an **Insights** button. Choose the period and, if you like, one branch. Each chart has a
**Copy as image** button and a **Show the numbers as a table** link; **Download as CSV** saves the series.
From the command line: `launchway insights --flight shop --from 2026-11-01 --csv shop.csv`. For an agent, the
`get_insights` tool; over HTTP, `GET /api/v1/insights`.

## Scrape the metrics

Every server, Station and launch Station serves `/metrics` on a port of its own. It is never the port of the
web UI and the API, and the Ingress or HTTPRoute never points at it.

With the Helm chart the port is `metrics.port` (default `9102`) on the pod's address, and a network policy lets
no pod reach it until you name who may, in `networkPolicy.metrics.from`:

```yaml
networkPolicy:
  metrics:
    from:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
metrics:
  serviceMonitor: { enabled: true, labels: { release: prometheus } }   # needs the Prometheus Operator's API
  podMonitor: { enabled: true, labels: { release: prometheus } }
  token:
    existingSecret: launchway-metrics   # optional: the scraper must send this bearer token
```

The ServiceMonitor and PodMonitor are rendered only when you ask for them, and the chart fails with a clear
message if the cluster does not serve their API. Without the Operator, set `metrics.scrapeAnnotations: true` for
scrapers that find pods by annotation, or scrape the pod addresses yourself.

Without the chart, set `observability.metrics.listen` (default `127.0.0.1:9102`) in the server's file, and
start each Station with `--metrics-listen :9102`. A bearer token is read from the environment variable named
by `observability.metrics.token_env`, or from `LAUNCHWAY_METRICS_TOKEN`.

Metrics hold no secret. No repository, branch, person or run id is a label. Flight names appear only in one extra
metric, and only if you turn on `observability.metrics.name_labels`. Metrics are installation-wide: give them
to operators. Per-Flight and per-Squadron views are the Insights pages.

Ready dashboards and alert rules for Grafana and Prometheus are in `deploy/dashboards`.

## Scale Stations and servers

`runner.autoscaling` and `server.autoscaling` create a HorizontalPodAutoscaler. Resource metrics work with the
cluster's metrics API. The queue depth is the better signal for Stations; with an adapter that serves
Prometheus metrics to the autoscaler, add it as an external metric:

```yaml
runner:
  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 20
    targetCPUUtilizationPercentage:
    extraMetrics:
      - type: External
        external:
          metric:
            name: launchway_queue_depth
            selector: { matchLabels: { pool: default } }
          target: { type: AverageValue, averageValue: "2" }   # two waiting steps for each Station
    behavior:
      scaleDown: { stabilizationWindowSeconds: 600 }
```

A Station that scales in finishes or hands back its steps first: keep `terminationGracePeriodSeconds` long
enough for them.

## Send traces

Turn tracing on with an address (and, if the collector needs a key, a Secret):

```yaml
tracing:
  enabled: true
  endpoint: https://collector.observability.svc:4318/v1/traces
  headers: { existingSecret: collector-key }
  sampleRatio: 0.2
  viewerURLTemplate: https://traces.example.com/trace/{trace_id}
```

The servers send through the same egress rules as every outbound request, so allow the collector's address in
`egress.allow` and in `networkPolicy.server.extraEgress`. Stations send their spans to the same address from the
environment variables the chart sets (`LAUNCHWAY_TRACING_ENDPOINT`, `LAUNCHWAY_TRACING_HEADERS`,
`LAUNCHWAY_TRACING_SAMPLE_RATIO`); they are not behind the server's egress guard.

A run is one trace, whose id is worked out from the run's id: the run's span, a span for each time a step waited,
a span for each step, and, from the Station, a span for the commands and one for collecting reports. A deploy is
a trace of its own. Server requests continue the trace a Station sends in a `traceparent` header. The run page
shows the trace id with a copy button, and a link when `viewerURLTemplate` is set.

A span can carry only the attributes of an allow-list: the step's name, the attempt, the outcome, the pool, the
route template, the method and the status. A command line, an environment value, a token, a secret or a
repository's contents has no place to go. Launchway writes the OTLP/HTTP JSON encoding itself, so it needs no
tracing library. Not yet separate spans: the pod start and the checkout, which are part of the Station's span of
the commands.

See also the [metrics reference](../reference/metrics.md), [Server configuration](../reference/server-config.md#observability)
and [Helm values](../reference/helm-values.md#metrics).
