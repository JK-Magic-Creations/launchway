<!-- Generated from the metrics registry (internal/metrics/catalog.go). Do not edit by hand: run
     LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/metrics -run TestReferencePageMatchesRegistry -->

# Metrics reference

Every metric the servers, Stations and the deploy runner expose on the metrics address, in the text exposition format. See [Metrics and traces](../howto/metrics-and-traces.md) for how to scrape them.

Labels come from small closed sets. A repository, a branch, a person or a run id is never a label. Every metric holds at most 500 distinct label sets (the Flight metric 300); more are dropped and counted in `launchway_metrics_series_dropped_total`.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `launchway_approval_oldest_age_seconds` | gauge | none | Age of the oldest approval still waiting; 0 when none waits. |
| `launchway_approvals_waiting` | gauge | none | Approvals waiting for a decision now. |
| `launchway_build_info` | gauge | `version`, `go_version` | Always 1; the version and Go version are its labels. |
| `launchway_cache_bytes_total` | counter | `direction` | Bytes of build cache moved; direction is restored or saved. |
| `launchway_cache_lookups_total` | counter | `result` | Build cache lookups; result is hit or miss. |
| `launchway_data_free_bytes` | gauge | none | Free bytes on the volume holding the server's data directory (where hosted repositories and the working files are kept); absent where the system does not say. |
| `launchway_data_total_bytes` | gauge | none | Size in bytes of the volume holding the server's data directory. |
| `launchway_db_pool_connections` | gauge | `state` | Database connections held by this process; state is open, in_use or idle. |
| `launchway_db_pool_wait_seconds_total` | counter | none | Total time queries waited for a free database connection. |
| `launchway_db_pool_wait_total` | counter | none | Times a query had to wait for a free database connection. |
| `launchway_db_query_duration_seconds` | histogram | `family` | Database statement durations by statement family: select, insert, update, delete, other. Never the statement text. |
| `launchway_deploys_total` | counter | `verdict` | Deploys closed, by verdict: deployed, failed, rolled_back, rollback_failed or abandoned. |
| `launchway_flight_runs_finished_total` | counter | `flight`, `outcome` | Runs finished per Flight. Recorded only when observability.metrics.name_labels is on (default off), and held to 300 series. |
| `launchway_git_operation_duration_seconds` | histogram | `operation` | Time a Git operation took, by operation. |
| `launchway_git_operations_total` | counter | `operation`, `outcome` | Git operations served or made: operation is fetch (served to a client), push (received) or checkout (a Station's clone); outcome is ok or error. |
| `launchway_git_transferred_bytes_total` | counter | `operation`, `direction` | Bytes moved by Git operations; direction is in (received) or out (sent). |
| `launchway_go_goroutines` | gauge | none | Goroutines in this process. |
| `launchway_go_heap_alloc_bytes` | gauge | none | Bytes of heap in use in this process. |
| `launchway_http_request_duration_seconds` | histogram | `route`, `method` | Time to answer an HTTP request, by route template and method. |
| `launchway_http_requests_in_flight` | gauge | none | HTTP requests being served now. |
| `launchway_http_requests_total` | counter | `route`, `method`, `class` | HTTP requests answered, by route template (never the path), method and status class (2xx, 3xx, 4xx, 5xx). |
| `launchway_leases_active` | gauge | `pool` | Step leases held by Stations now, by pool. |
| `launchway_metrics_series_dropped_total` | counter | `metric` | Label sets refused because their metric already held its cap of distinct series. |
| `launchway_migrations_applied_total` | counter | none | Database migrations this process applied while starting. |
| `launchway_mirror_lag_seconds_max` | gauge | none | The longest time any mirror has gone without a successful synchronisation; 0 when there are none. |
| `launchway_mirror_syncs_total` | counter | `outcome` | Mirror synchronisations, by outcome (ok or error). |
| `launchway_queue_depth` | gauge | `pool` | Steps waiting for a Station, by Station pool (the Station's pool name, or default). |
| `launchway_queue_oldest_wait_seconds` | gauge | `pool` | How long the longest-waiting queued step has waited, by pool; 0 when the queue is empty. |
| `launchway_queue_wait_seconds` | histogram | `pool` | Time a step waited from being queued until a Station took it, by pool. |
| `launchway_rollbacks_total` | counter | none | Rollbacks started, automatic or asked for. |
| `launchway_run_duration_seconds` | histogram | `outcome` | Time from a run starting to it finishing, by outcome. |
| `launchway_runs_finished_total` | counter | `outcome` | Runs finished, by outcome (succeeded, failed, canceled). |
| `launchway_runs_started_total` | counter | none | Runs started. |
| `launchway_schema_version` | gauge | none | The database's schema version. |
| `launchway_sign_ins_total` | counter | `method`, `outcome` | Sign-in attempts; method is local or oidc; outcome is ok or denied. |
| `launchway_station_heartbeats_missed_total` | counter | none | Leases that expired because their Station stopped reporting. |
| `launchway_stations_online` | gauge | `pool` | Stations that reported in recently, by pool. |
| `launchway_step_duration_seconds` | histogram | `outcome` | Time a step ran, by outcome. |
| `launchway_step_room_waits_total` | counter | `reason` | Times a Station could not start a step pod for lack of room and the step was queued to try again; reason is quota_full or cluster_full. |
| `launchway_steps_finished_total` | counter | `outcome` | Steps finished, by outcome (succeeded, failed, canceled, skipped). |
| `launchway_steps_started_total` | counter | none | Steps (Sorties) started. |
| `launchway_token_uses_total` | counter | `kind` | Requests authenticated by a token, by kind (session, api, station, service). |
| `launchway_trace_spans_total` | counter | `outcome` | Trace spans handed to the exporter; outcome is exported or dropped (queue full or the endpoint failed). |
| `launchway_uptime_seconds` | gauge | none | Seconds since this process started. |
| `launchway_webhook_deliveries_total` | counter | `direction`, `outcome` | Webhook deliveries; direction is in (received) or out (sent); outcome is accepted, rejected or failed. |
| `launchway_worker_last_success_timestamp_seconds` | gauge | `worker` | When each background worker last finished a pass without error, as Unix seconds. A worker that stops shows a timestamp that stops moving. |
