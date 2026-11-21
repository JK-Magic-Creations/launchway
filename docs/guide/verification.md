# Verifying a deployment

A step that watches a deployment's metrics and logs for a window of time and decides passed, failed or inconclusive.

A `kind: verify` step watches a deployment (or an environment) for a window of time, reads its
metrics and logs, and decides **passed**, **failed** or **inconclusive**. The judging is built into
Launchway: plain statistics and log clustering, no AI service and nothing to install. A model can
explain the verdict (see [AI models](ai.md)), but the statistics decide.

## A canary verified with Prometheus and Loki, then promoted or aborted

```yaml
steps:
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop, until: paused }   # stop at the canary's pause

  verify-canary:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod          # watch this deployment (its environment, service, pods)
    delay: 1m                        # let the canary warm up first
    duration: 10m
    interval: 1m
    sensitivity: medium              # low | medium | high
    baseline: 3                      # also compare with the last 3 passed verifications here
    on_fail: abort                   # fail | abort (abort the rollout now) | rollback
    on_inconclusive: pause           # fail | pass | pause (wait for a person, like an approval)
    params:                          # typed values, never pasted into a query as text
      ns:     { deployment: namespace }
      canary: { deployment: canary_hash }
      stable: { deployment: stable_hash }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{canary}}",code=~"5.."}[1m]))
                / sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{canary}}"}[1m]))'
        stable_query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{stable}}",code=~"5.."}[1m]))
                / sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{stable}}"}[1m]))'
        max: 0.01                    # a fixed bound
        fail_fast: { max: 0.2 }      # fail the whole verification at once
      - name: p99-latency
        source: prometheus
        query: 'histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{pod_template_hash="{{canary}}"}[1m])))'
        stable_query: 'histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{pod_template_hash="{{stable}}"}[1m])))'
        direction: higher-is-worse   # or lower-is-worse (throughput) or either
        failures: 2                  # failing intervals in a row that fail the metric (default 2)
    logs:
      - name: app
        source: loki
        query: '{namespace="{{ns}}", pod_template_hash="{{canary}}"}'
        stable_query: '{namespace="{{ns}}", pod_template_hash="{{stable}}"}'

  promote:
    kind: rollout
    needs: verify-canary             # runs only when the verification passed
    deployment: deploy-prod
    action: promote-full

  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]                 # runs when the verification (and so promote) failed
    abort_first: true
```

A healthy canary passes and the rollout step promotes it. A canary whose error rate climbs fails
the verification: `on_fail: abort` aborts the rollout through the same command a person's Abort
button gives (recorded, audited, applied by the deploy runner), and the rollback step reverts the
Git change. Placeholders `{{name}}` take the step's `params`: `{ run: sha }`, `{ input: name }`,
`{ output: step.key }`, or a fact of the deployment (`namespace`, `rollout`, `application`,
`version`, `previous_version`, `environment`, `service`, `canary_hash`, `stable_hash`). Each value
is checked and escaped for the query language it goes into (PromQL and LogQL string literals,
Elasticsearch JSON string values) or sent as a typed parameter (SQL); a placeholder outside a quoted
string is refused when the pipeline is checked.

A verify step can also stand alone: `environment: prod` and `service: shop` instead of
`deployment:`, to watch what runs now (after a deploy done elsewhere, or on a schedule).

## Log verification with ClickHouse

SQL and HTTP sources run only statements their operator wrote, by name; a pipeline passes typed
parameters and never writes SQL. On the deploy runner:

```yaml
health_sources:
  clickhouse:
    type: clickhouse
    url: http://clickhouse.logging.svc:8123
    projects: [shop]
    auth: { user: launchway_ro, secret: { store: vault, key: ci/clickhouse, field: password } }
    statements:
      service_errors:
        kind: logs
        params: { service: String, version: String }
        sql: >
          SELECT ts AS t, message FROM logs
          WHERE service = {service:String} AND version = {version:String} AND level = 'error'
            AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)}
          ORDER BY ts
```

and in the pipeline:

```yaml
  verify-logs:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    duration: 15m
    interval: 1m
    params:
      service: { deployment: service }
      version: { deployment: version }
    logs:
      - name: errors
        source: clickhouse
        statement: service_errors
        errors: all                  # every line this statement returns is an error
```

The lines are clustered into templates and compared with what the last passed verifications of
`shop` in `prod` saw: a new error template fails the verification (at medium sensitivity, once it
has at least 3 lines), as does a known one appearing unusually often.

## Health sources

Operators configure where the data is; pipelines only name it. The servers list the names, types
and projects (`verification.sources`; every server lists the same ones), and the deploy runners
that serve the environment's target hold the rest (`health_sources`), because they are the ones next
to the monitoring systems and they never run anyone's script:

```yaml
# server: launchway.yaml
verification:
  sources:
    - { name: prometheus, type: prometheus, projects: ["*"] }
    - { name: loki,       type: loki,       projects: [shop, "mcp-*"] }
    - { name: clickhouse, type: clickhouse, projects: [shop] }

# deploy runner: runner.yaml
health_sources:
  prometheus:
    type: prometheus                  # also Thanos, Mimir, VictoriaMetrics
    url: http://prometheus-operated.monitoring.svc:9090
    projects: ["*"]
  loki:
    type: loki
    url: http://loki-gateway.monitoring.svc
    headers: { X-Scope-OrgID: platform }
    auth: { type: bearer, secret: { store: vault, key: ci/loki, field: token } }
    projects: [shop, "mcp-*"]
    timeout: 15s
    limits: { max_points: 500, max_lines: 5000, max_bytes: 8388608 }
```

Types: `prometheus`, `loki`, `clickhouse`, `elasticsearch` / `opensearch` (with `indices`, the
only indices pipelines may search), `postgres` (named statements in a read-only transaction) and
`http` (named requests to any JSON API, with a path into the answer). With the Helm chart,
`delivery.verification.sources` fills both sides. Credentials are only secret references, read from
the deploy runner's own stores for each query.

**What a pipeline cannot do with a health source.** Use one its project is not listed for (checked
when the pipeline is validated, and again on the deploy runner); run SQL of its own; search an
index not allowed; change anything (PromQL and LogQL are read APIs; ClickHouse statements run with
`readonly=1`; PostgreSQL ones in a `READ ONLY` transaction; HTTP requests go to the configured
host only, without following redirects); or read without bounds (each query has a timeout, at most
one series per metric query, and limits on points, lines and bytes; log lines are redacted before
they are clustered, stored or shown).

## How it decides

Each interval, a metric's value is the mean of its readings, checked against `fail_fast` bounds,
`max`/`min`, the stable version in the same interval, and the baseline learned from earlier passed
verifications of the same environment and service, in standard deviations:

| sensitivity | fails beyond | and at least | log clusters match at | unusual frequency |
|---|---|---|---|---|
| high   | 2 sd   | 5% of the reference   | 90% of tokens | 2 times |
| medium | 3 sd   | 10% of the reference  | 75% | 3 times |
| low    | 4.5 sd | 25% of the reference  | 60% | 5 times |

A metric fails after `failures` failing intervals in a row. Too little data is **inconclusive**,
never a silent pass: an interval needs `min_points` readings, a metric needs `min_intervals`
intervals with data (default half), and a verification with no check that could run (no bound, no
stable query, no baseline yet) is inconclusive. Logs are clustered with the Drain algorithm and
classed as known, unknown (new) or unexpected frequency; only error clusters fail. Every interval's
data and the verdict are events, so any server re-derives the same verdict. The limits of these
statistics (when they pass a bad version or fail a good one: short windows, low traffic, a
regressed baseline, seasonality, warm-up) are written down in
[internal/verify/doc.go](https://github.com/JK-Magic-Creations/launchway/blob/main/internal/verify/doc.go).

**Marking a verification passed or failed.** An operator of the project may mark a running or
paused verification passed or failed, with a reason (`verification.override` in the Logbook);
on a protected environment the person who started the run may not mark it passed. A pass marked by
a person does not become part of the baseline.

**The run page** has a Verification tab and panel: the intervals as a timeline, a chart per metric
with the canary, the stable version's band and the baseline's band and the points that failed, the
log clusters with counts and sample lines (new ones first), the verdict in words ("error-rate:
0.042 against the stable version's 0.003, 6.1 standard deviations above"), and the buttons the
viewer may use. **The Environments page** lists each service's recent verifications. API:
`GET /api/v1/runs/{run}/verifications`, `GET /api/v1/verifications?environment=&project=&service=`,
`POST /api/v1/runs/{run}/steps/{step}/verify/override`, `GET /api/v1/health-sources` (names only).
CLI: `launchway verifications list|show|mark`, `launchway health-sources`.

Verify steps need schema 19: use them once every server and deploy runner is upgraded, and take
them out before rolling back (see [docs/development/migrations.md](../development/migrations.md)).
