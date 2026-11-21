---
title: Interface parity
---

# Interface parity

Everything Launchway can do is available through the HTTP API, the MCP server and the command line, with the same permissions and the same checks, and every kind of configuration is planned as a Terraform resource. This page is generated from the table the tests check (`internal/parity`): a route in the API without a row, a row naming an MCP tool or command that does not exist, or a growing number of gaps fails the build.

A gap is something an interface lacks today; the number may only go down. An exemption is an operation that is not for people or agents, with the reason.

| Operation | MCP tool | CLI command | Terraform | Status |
|---|---|---|---|---|
| `GET /.well-known/oauth-protected-resource/mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /api/runner/v1/artifacts/begin` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `PUT /api/runner/v1/artifacts/chunk` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/artifacts/commit` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/artifacts/download` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/caches/begin` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `PUT /api/runner/v1/caches/chunk` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/caches/commit` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/caches/discard` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/caches/download` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/caches/lookup` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/command-done` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/commands` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/lease` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/observe` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/pull-request` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/verify/interval` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/verify/lease` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/deploy/watch` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/finish` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/heartbeat` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/lease` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/leave` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/logs` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/runs` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v1/start` | — | — | — | exempt: the runner protocol: only runners call it, with their own credentials |
| `POST /api/runner/v2/join` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /api/runner/v2/rotate` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /api/runner/v2/token` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `OPTIONS /api/sdk/v1/evaluate` | — | — | — | exempt: the application protocol: SDKs call it with an SDK key, which only reads toggles; it is not an operation for people, agents or Terraform |
| `POST /api/sdk/v1/evaluate` | — | — | — | exempt: the application protocol: SDKs call it with an SDK key, which only reads toggles; it is not an operation for people, agents or Terraform |
| `GET /api/sdk/v1/ruleset` | — | — | — | exempt: the application protocol: SDKs call it with an SDK key, which only reads toggles; it is not an operation for people, agents or Terraform |
| `GET /api/sdk/v1/stream` | — | — | — | exempt: the application protocol: SDKs call it with an SDK key, which only reads toggles; it is not an operation for people, agents or Terraform |
| `POST /api/sdk/v1/telemetry` | — | — | — | exempt: the application protocol: SDKs call it with an SDK key, which only reads toggles; it is not an operation for people, agents or Terraform |
| `GET /api/v1/account` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `POST /api/v1/account/password` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `POST /api/v1/account/recovery-codes` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `DELETE /api/v1/account/second-factor` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `POST /api/v1/account/second-factor` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `POST /api/v1/account/second-factor/confirm` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `POST /api/v1/account/sign-out-everywhere` | — | — | — | exempt: a person changes their own password and second factor in the browser, proving who they are again; a token or agent acts as a token, not as the person |
| `GET /api/v1/admin/tokens` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `DELETE /api/v1/admin/tokens/{id}` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `GET /api/v1/ai/models` | `list_config (kind models)` | `models` | `launchway_model` | equal |
| `GET /api/v1/approvals` | `list_approvals` | — | — | gap: launchway has no approvals command yet |
| `GET /api/v1/artifact-store` | — | `blobs status` | — | gap: blobs status reads the database directly; no MCP tool |
| `GET /api/v1/audit` | — | — | — | gap: the audit trail is read in the UI; no MCP tool or CLI command yet |
| `POST /api/v1/auth/link` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/auth/link/begin` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/auth/link/complete` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/auth/local/second-factor` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/auth/local/signin` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/auth/setup-now` | — | — | — | exempt: asks an installation that runs with no sign-in for built-in accounts: a credential flow done from the warning on the page; the console covers it as launchway users setup-code and auth.mode |
| `GET /api/v1/cache-policy` | `get_cache_policy` | `caches policy get` | `not needed by terraform` | equal |
| `PUT /api/v1/cache-policy` | `set_cache_policy` | `caches policy set` | — | exempt: MCP and CLI manage the cache policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind) |
| `GET /api/v1/components` | — | — | — | gap: the component inventory is UI only so far |
| `GET /api/v1/config` | `get_config_overview` | `config overview` | — | equal |
| `POST /api/v1/config/apply` | `apply_config` | `config apply` | — | equal |
| `POST /api/v1/config/check/repository` | `check_repository` | `config check-repository` | — | equal |
| `POST /api/v1/config/check/signin` | `check_signin` | `config check-signin` | — | equal |
| `GET /api/v1/config/effective/{project}` | `get_effective_config` | `config effective` | — | equal |
| `GET /api/v1/config/export` | `export_config` | `config export` | — | equal |
| `GET /api/v1/config/history` | `get_config_history` | `config history` | — | equal |
| `POST /api/v1/config/import` | `import_config` | `config import` | — | equal |
| `GET /api/v1/config/imports` | `list_config_imports` | `config imports` | — | equal |
| `POST /api/v1/config/validate` | `validate_config` | `config validate` | — | equal |
| `GET /api/v1/config/{kind}` | `list_config` | `config list` | `data sources and resources of every kind` | equal |
| `POST /api/v1/config/{kind}` | `put_config (version -1)` | `config create` | `one resource per kind (launchway_project`, `launchway_library`, `...)` | equal |
| `DELETE /api/v1/config/{kind}/{obj}` | `delete_config` | `config delete` | `destroy of the resource` | equal |
| `GET /api/v1/config/{kind}/{obj}` | `get_config` | `config get` | `terraform import by name` | equal |
| `PUT /api/v1/config/{kind}/{obj}` | `put_config` | `config set` | `create or update of the resource` | equal |
| `GET /api/v1/config/{kind}/{obj}/history` | `get_config_history` | `config history` | `not needed by terraform` | equal |
| `POST /api/v1/config/{kind}/{obj}/restore` | `restore_config` | `config restore` | `not needed by terraform` | equal |
| `GET /api/v1/connections` | `list_connections` | `repos connection list` | — | equal |
| `POST /api/v1/connections` | `add_connection` | `repos connection add` | — | equal |
| `POST /api/v1/connections/token` | — | `repos connection add` | — | exempt: pasting a token into a browser form is a human convenience; scripts and agents give a credential reference (an environment variable or file) with the same connection add |
| `DELETE /api/v1/connections/{name}` | `remove_connection` | `repos connection remove` | — | equal |
| `GET /api/v1/deployments` | `get_deployments` | — | — | gap: launchway has no deployments command yet |
| `GET /api/v1/encryption` | `get_encryption_status` | `encryption status` | — | exempt: a read-only report of the root key and sealing progress; nothing to configure, since the root key is a bootstrap setting held outside the database |
| `GET /api/v1/encryption/root-key-copy` | — | — | — | exempt: whether a recovery copy of the root key was taken is shown in the browser guide; the audit trail records it for every interface |
| `POST /api/v1/encryption/root-key-copy` | — | — | — | exempt: taking a copy of the root key asks a person to type their password and a code again; a token or agent must never be able to export the key (the server's own console has launchway encryption) |
| `GET /api/v1/environments` | `list_config (kind environments)` | `environments list` | `launchway_environment` | equal |
| `GET /api/v1/events` | `list_events` | `events list` | `not needed by terraform` | equal |
| `GET /api/v1/events/{id}` | `get_event` | `events show` | `not needed by terraform` | equal |
| `POST /api/v1/events/{id}/replay` | `replay_event` | `events replay` | `not needed by terraform` | equal |
| `GET /api/v1/health-sources` | `list_config (kind health-sources)` | `health-sources` | `launchway_health_source` | equal |
| `GET /api/v1/images` | `list_images` | — | — | gap: launchway has no images command yet |
| `POST /api/v1/imports` | `import_repositories` | `repos import` | — | equal |
| `POST /api/v1/imports/verify` | `verify_repositories` | `repos verify` | — | equal |
| `GET /api/v1/info` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /api/v1/launch-lines` | — | — | — | exempt: a read model for the Home page, worked out from a run's steps and deployments, which list_runs, get_run and get_deployments already give in full |
| `GET /api/v1/libraries` | `list_config (kind libraries)` | `libraries list` | `launchway_library` | equal |
| `GET /api/v1/libraries/{library}/templates` | `list_templates` | — | — | gap: launchway has no templates command yet |
| `GET /api/v1/me` | — | — | — | exempt: the caller's own identity: the token is the credential for MCP and the CLI |
| `GET /api/v1/mirrors` | `list_mirrors` | `repos mirror status` | — | equal |
| `GET /api/v1/notifications` | `list_notifications` | `notifications` | — | equal |
| `GET /api/v1/owners/{owner}/hooks` | `list_webhooks` | `repos hooks list` | `planned: launchway_webhook` | equal |
| `POST /api/v1/owners/{owner}/hooks` | `add_webhook` | `repos hooks add` | `planned: launchway_webhook` | equal |
| `DELETE /api/v1/owners/{owner}/hooks/{id}` | `delete_webhook` | `repos hooks delete` | `planned: launchway_webhook` | equal |
| `PATCH /api/v1/owners/{owner}/hooks/{id}` | `update_webhook` | `repos hooks update` | `planned: launchway_webhook` | equal |
| `GET /api/v1/owners/{owner}/hooks/{id}/deliveries` | `list_webhook_deliveries` | `repos hooks deliveries` | `planned: launchway_webhook` | equal |
| `POST /api/v1/owners/{owner}/hooks/{id}/deliveries/{delivery}/redeliver` | `redeliver_webhook` | `repos hooks redeliver` | `planned: launchway_webhook` | equal |
| `POST /api/v1/owners/{owner}/hooks/{id}/test` | `test_webhook` | `repos hooks test` | `planned: launchway_webhook` | equal |
| `GET /api/v1/policies` | `list_config (kind policies)` | `policy list` | `launchway_policy` | equal |
| `GET /api/v1/projects` | `list_projects` | `projects list` | — | equal |
| `GET /api/v1/projects/{name}` | `get_project` | `projects get` | — | equal |
| `DELETE /api/v1/projects/{name}/cache-policy` | `set_cache_policy` | `caches policy clear` | — | exempt: MCP and CLI manage the cache policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind) |
| `GET /api/v1/projects/{name}/cache-policy` | `get_cache_policy` | `caches policy get` | `not needed by terraform` | equal |
| `PUT /api/v1/projects/{name}/cache-policy` | `set_cache_policy` | `caches policy set` | — | exempt: MCP and CLI manage the cache policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind) |
| `DELETE /api/v1/projects/{name}/caches` | `delete_cache` | `caches delete` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/caches` | `list_caches` | `caches list` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/caches/{id}` | `delete_cache` | `caches delete` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/coverage` | — | `runs coverage` | — | gap: no MCP tool reads coverage yet |
| `POST /api/v1/projects/{name}/draft-runs` | `start_run (pipeline_text)` | — | — | gap: launchway runs start has no flag for a draft run yet |
| `GET /api/v1/projects/{name}/event-sources` | `list_event_sources` | `event-sources list` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/event-sources` | — | `event-sources create` | — | exempt: the signing secret is shown once and must not enter an agent's context; the console and the web interface make and rotate sources (launchway event-sources) |
| `DELETE /api/v1/projects/{name}/event-sources/{source}` | `delete_event_source` | `event-sources delete` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/event-sources/{source}` | — | `event-sources change` | — | exempt: the signing secret is shown once and must not enter an agent's context; the console and the web interface make and rotate sources (launchway event-sources) |
| `POST /api/v1/projects/{name}/event-sources/{source}/test` | `test_event_source` | `event-sources test` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/git-hook` | — | — | — | exempt: called by Git hosts and other systems with their own proof, not by people |
| `GET /api/v1/projects/{name}/inputs` | `get_project` | `projects get` | — | equal |
| `GET /api/v1/projects/{name}/lanes` | `list_lanes` | `runs lanes` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/pipeline` | `get_project` | `config get projects` | — | equal |
| `POST /api/v1/projects/{name}/pipeline` | — | — | — | gap: the pipeline editor commits to Git; agents and the CLI edit the file in the repository and check it with validate_pipeline and launchway validate |
| `GET /api/v1/projects/{name}/refs` | — | — | — | exempt: branch and tag picker of the UI; start_run and runs start take the ref as text |
| `GET /api/v1/projects/{name}/registry` | `list_registry_images` | `registry list` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/registry/image` | `delete_registry_image` | `registry delete` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/registry/image` | `inspect_registry_image` | `registry inspect` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/registry/images` | `list_registry_images` | `registry list`, `registry tags` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/registry/policy` | `get_registry_policy` | `registry policy get` | `not needed by terraform` | equal |
| `PUT /api/v1/projects/{name}/registry/policy` | `set_registry_policy` | `registry policy set` | — | exempt: MCP and CLI manage the registry policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind) |
| `GET /api/v1/projects/{name}/registry/retention` | `preview_registry_retention` | `registry retention` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/registry/retention` | `run_registry_retention` | `registry retention` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/registry/tag` | `delete_registry_image` | `registry delete` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/rollback` | `get_rollback_options` | `deployments options` | — | equal |
| `POST /api/v1/projects/{name}/rollbacks` | `rollback_deployment` | `deployments rollback` | — | equal |
| `GET /api/v1/projects/{name}/run-trigger-allows` | `list_run_trigger_allows` | `run-triggers allowed` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/run-trigger-allows` | `allow_run_triggers` | `run-triggers allow` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/run-trigger-allows/{downstream}` | `revoke_run_triggers` | `run-triggers revoke` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/run-triggers` | `list_run_triggers` | `run-triggers list` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/run-triggers` | `create_run_trigger` | `run-triggers create` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/run-triggers/{trigger}` | `delete_run_trigger` | `run-triggers delete` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/runs` | `start_run` | `runs start` | — | equal |
| `GET /api/v1/projects/{name}/schedules` | `get_project` | `projects get` | — | equal |
| `GET /api/v1/projects/{name}/security` | `get_security_findings` | — | — | gap: launchway has no findings command yet |
| `POST /api/v1/projects/{name}/sources/{source}` | — | — | — | exempt: the address other systems call, signed with the source's secret: not an operation for people or agents (the console tests it with launchway event-sources test) |
| `GET /api/v1/projects/{name}/tests` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/projects/{name}/tests/history` | `get_test_results` | `runs tests` | — | equal |
| `POST /api/v1/projects/{name}/toggle-apply` | `apply_toggles_file` | `toggles apply` | `the file is the declarative form: toggles.yaml applied with launchway toggles apply` | equal |
| `GET /api/v1/projects/{name}/toggle-changes` | `list_toggle_changes_waiting` | `toggles waiting` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/approve` | `approve_toggle_change` | `toggles approve` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/reject` | `reject_toggle_change` | `toggles reject` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/revert` | `revert_toggle_change` | `toggles revert` | `not needed by terraform` | equal |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/withdraw` | `withdraw_toggle_change` | `toggles withdraw` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/toggle-file` | `export_toggles_file` | `toggles export` | `the file is the declarative form: toggles.yaml applied with launchway toggles apply` | equal |
| `GET /api/v1/projects/{name}/toggle-keys` | `list_toggle_keys` | `toggles keys` | `planned: data source launchway_toggle_keys` | equal |
| `POST /api/v1/projects/{name}/toggle-keys` | — | `toggles keys` | `planned: resource launchway_toggle_key (the secret as a write-only attribute)` | exempt: an SDK key is a secret and is shown once: it is made in the web page or with the CLI, never through an agent's context, which would keep it |
| `DELETE /api/v1/projects/{name}/toggle-keys/{id}` | `revoke_toggle_key` | `toggles keys` | `planned: resource launchway_toggle_key` | equal |
| `POST /api/v1/projects/{name}/toggle-keys/{id}/rotate` | — | `toggles keys` | `planned: resource launchway_toggle_key (the secret as a write-only attribute)` | exempt: an SDK key is a secret and is shown once: it is made in the web page or with the CLI, never through an agent's context, which would keep it |
| `POST /api/v1/projects/{name}/toggle-plan` | `plan_toggles_file` | `toggles plan` | `the file is the declarative form: toggles.yaml applied with launchway toggles apply` | equal |
| `POST /api/v1/projects/{name}/toggle-revert-since` | `revert_toggle_changes_since` | `toggles revert-since` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/toggle-safeguards` | `get_toggle_safeguards` | `toggles safeguards` | `planned: data source launchway_toggle_safeguards` | equal |
| `PUT /api/v1/projects/{name}/toggle-safeguards` | `set_toggle_safeguards` | `toggles safeguards` | `planned: resource launchway_toggle_safeguards` | equal |
| `GET /api/v1/projects/{name}/toggle-schedules` | `list_toggle_schedules` | `toggles schedules` | `planned: data source launchway_toggle_schedules` | equal |
| `DELETE /api/v1/projects/{name}/toggle-schedules/{id}` | `cancel_toggle_schedule` | `toggles schedules` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/toggle-segments` | `list_toggle_segments` | `toggles segments` | `planned: data source launchway_toggle_segments` | equal |
| `DELETE /api/v1/projects/{name}/toggle-segments/{segment}` | `delete_toggle_segment` | `toggles segments` | `planned: resource launchway_toggle_segment` | equal |
| `PUT /api/v1/projects/{name}/toggle-segments/{segment}` | `save_toggle_segment` | `toggles segments` | `planned: resource launchway_toggle_segment` | equal |
| `GET /api/v1/projects/{name}/toggle-timeline` | `toggle_timeline` | `toggles timeline` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/toggles` | `list_toggles` | `toggles list` | `planned: data source launchway_toggles` | equal |
| `POST /api/v1/projects/{name}/toggles` | `create_toggle` | `toggles create` | `planned: resource launchway_toggle` | equal |
| `DELETE /api/v1/projects/{name}/toggles/{key}` | `delete_toggle` | `toggles create` | `planned: resource launchway_toggle` | equal |
| `GET /api/v1/projects/{name}/toggles/{key}` | `get_toggle` | `toggles get` | `planned: data source launchway_toggle` | equal |
| `PATCH /api/v1/projects/{name}/toggles/{key}` | `update_toggle` | `toggles create` | `planned: resource launchway_toggle` | equal |
| `PUT /api/v1/projects/{name}/toggles/{key}/environments/{env}` | `set_toggle_state` | `toggles set` | `planned: resource launchway_toggle_environment` | equal |
| `POST /api/v1/projects/{name}/toggles/{key}/evaluate` | `evaluate_toggle` | `toggles evaluate` | `planned: data source launchway_toggle_evaluation` | equal |
| `GET /api/v1/projects/{name}/toggles/{key}/history` | `toggle_history` | `toggles history` | `not needed by terraform` | equal |
| `DELETE /api/v1/projects/{name}/toggles/{key}/lock` | `unlock_toggle` | `toggles unlock` | `planned: resource launchway_toggle (lock block)` | equal |
| `PUT /api/v1/projects/{name}/toggles/{key}/lock` | `lock_toggle` | `toggles lock` | `planned: resource launchway_toggle (lock block)` | equal |
| `GET /api/v1/projects/{name}/toggles/{key}/usage` | `get_toggle_usage` | `toggles usage` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/variables` | `get_effective_variables` | `variables effective` | `planned: data source launchway_effective_variables` | equal |
| `POST /api/v1/projects/{name}/waivers` | — | `waivers grant` | — | gap: no MCP tool grants waivers yet |
| `POST /api/v1/projects/{name}/waivers/{waiver}/revoke` | — | `waivers revoke` | — | gap: no MCP tool revokes waivers yet |
| `POST /api/v1/projects/{name}/webhooks/{trigger}` | — | — | — | exempt: called by Git hosts and other systems with their own proof, not by people |
| `GET /api/v1/queue` | `list_queue` | `runs queue` | `not needed by terraform` | equal |
| `GET /api/v1/registry` | `registry_status` | `registry status` | `not needed by terraform` | equal |
| `GET /api/v1/registry/gc` | `registry_gc` | `registry gc` | `not needed by terraform` | equal |
| `POST /api/v1/registry/gc` | `registry_gc` | `registry gc` | `not needed by terraform` | equal |
| `GET /api/v1/registry/policy` | `get_registry_policy` | `registry policy get` | `not needed by terraform` | equal |
| `PUT /api/v1/registry/policy` | `set_registry_policy` | `registry policy set` | — | exempt: MCP and CLI manage the registry policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind) |
| `DELETE /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `PATCH /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repos/{owner}/{name}/branches/{branch}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repositories` | `list_repositories` | `repos list` | `planned: launchway_repository` | equal |
| `POST /api/v1/repositories` | `create_repository` | `repos create` | `planned: launchway_repository` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}` | `delete_repository` | `repos delete` | `planned: launchway_repository` | equal |
| `GET /api/v1/repositories/{owner}/{name}` | `get_repository` | `repos get` | `planned: launchway_repository` | equal |
| `PATCH /api/v1/repositories/{owner}/{name}` | `update_repository` | `repos update` | `planned: launchway_repository` | equal |
| `GET /api/v1/repositories/{owner}/{name}/archive/issues` | `list_archived_issues` | `repos archive list` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/archive/issues` | `import_archive` | `repos import-archive` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/archive/issues/{number}` | `get_archived_issue` | `repos archive show` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/blame` | `blame_repository_file` | `repos blame` | — | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/branches` | `delete_repository_branch` | `repos branches delete` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/branches` | `list_repository_branches` | `repos branches` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/branches` | `create_repository_branch` | `repos branches create` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/commits` | `list_repository_commits` | `repos commits` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/commits/{sha}` | `get_repository_commit` | `repos show` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/compare` | `compare_branches` | `repos compare` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/dependencies` | `list_dependencies` | `deps list` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/deploy-keys` | `list_deploy_keys` | `repos deploy-keys list` | `planned: launchway_deploy_key` | equal |
| `POST /api/v1/repositories/{owner}/{name}/deploy-keys` | `add_deploy_key` | `repos deploy-keys add` | `planned: launchway_deploy_key` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/deploy-keys/{id}` | `delete_deploy_key` | `repos deploy-keys delete` | `planned: launchway_deploy_key` | equal |
| `GET /api/v1/repositories/{owner}/{name}/diff` | `get_repository_diff` | `repos show` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/diff/patch` | `get_repository_diff` | `repos show` | — | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/files` | `delete_repository_file` | `repos delete-file` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/files` | `read_repository_file` | `repos files` | — | equal |
| `PUT /api/v1/repositories/{owner}/{name}/files` | `write_repository_file` | `repos put-file` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/hooks` | `list_webhooks` | `repos hooks list` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks` | `add_webhook` | `repos hooks add` | `planned: launchway_webhook` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/hooks/{id}` | `delete_webhook` | `repos hooks delete` | `planned: launchway_webhook` | equal |
| `PATCH /api/v1/repositories/{owner}/{name}/hooks/{id}` | `update_webhook` | `repos hooks update` | `planned: launchway_webhook` | equal |
| `GET /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries` | `list_webhook_deliveries` | `repos hooks deliveries` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries/{delivery}/redeliver` | `redeliver_webhook` | `repos hooks redeliver` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/test` | `test_webhook` | `repos hooks test` | `planned: launchway_webhook` | equal |
| `GET /api/v1/repositories/{owner}/{name}/merge-rules` | `get_pull_settings` | `pr rules` | `planned: launchway_merge_rule` | equal |
| `PUT /api/v1/repositories/{owner}/{name}/merge-rules` | `set_pull_settings` | `pr rules` | `planned: launchway_merge_rule` | equal |
| `GET /api/v1/repositories/{owner}/{name}/mirrors` | `list_mirrors` | `repos mirror status` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/mirrors` | `set_mirror` | `repos mirror add` | — | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/mirrors/{id}` | `change_mirror` | `repos mirror remove` | — | equal |
| `PATCH /api/v1/repositories/{owner}/{name}/mirrors/{id}` | `change_mirror` | `repos mirror pause` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/mirrors/{id}/sync` | `sync_mirror` | `repos mirror sync` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/protected-branches` | `get_protected_branches` | `repos protected-branches` | `planned: launchway_branch_protection` | equal |
| `PUT /api/v1/repositories/{owner}/{name}/protected-branches` | `set_protected_branches` | `repos protected-branches` | `planned: launchway_branch_protection` | equal |
| `GET /api/v1/repositories/{owner}/{name}/pull-settings` | `get_pull_settings` | `pr settings` | `planned: launchway_repository_merge_settings` | equal |
| `PUT /api/v1/repositories/{owner}/{name}/pull-settings` | `set_pull_settings` | `pr settings` | `planned: launchway_repository_merge_settings` | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls` | `list_pull_requests` | `pr list` | `planned: launchway_pull_request` | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls` | `create_pull_request` | `pr create` | `planned: launchway_pull_request` | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}` | `get_pull_request` | `pr view` | — | equal |
| `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}` | `update_pull_request` | `pr edit` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments` | `comment_on_pull_request` | `pr comment` | — | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}` | `update_pull_comment` | `pr comment` | — | equal |
| `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}` | `update_pull_comment` | `pr comment` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}/apply` | `update_pull_comment` | `pr apply` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/commits` | `get_pull_request` | `pr view` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/conversation` | `get_pull_request` | `pr view` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/files` | `get_pull_request` | `pr diff` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/merge` | `merge_pull_request` | `pr merge` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/patch` | `get_pull_request` | `pr diff` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/revert` | `revert_pull_request` | `pr revert` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests` | `review_pull_request` | `pr review` | — | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests/{reviewer}` | `review_pull_request` | `pr review` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews` | `review_pull_request` | `pr review` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews/{id}/dismiss` | `review_pull_request` | `pr review` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/raw` | `read_repository_file` | `repos cat` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/search` | `search_repository_code` | `repos search` | — | equal |
| `POST /api/v1/repositories/{owner}/{name}/stop-mirroring` | `stop_mirroring` | `repos mirror stop` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/tags` | `list_repository_tags` | `repos tags` | — | equal |
| `GET /api/v1/repositories/{owner}/{name}/updates/preview` | `preview_updates` | `updates preview` | — | equal |
| `GET /api/v1/rollbacks/review` | `list_rollback_reviews` | `deployments review list` | — | equal |
| `GET /api/v1/run-limits` | `get_run_limits` | `run-limits get` | — | exempt: MCP and CLI manage the installation limits; they are held in a table of their own, so Terraform cannot yet (backlog: make them a configuration kind) |
| `PUT /api/v1/run-limits` | `set_run_limits` | `run-limits set` | — | exempt: MCP and CLI manage the installation limits; they are held in a table of their own, so Terraform cannot yet (backlog: make them a configuration kind) |
| `GET /api/v1/runner-join-tokens` | — | — | — | gap: join tokens are made from the UI wizard or the CLI; no MCP tool and no CLI list yet |
| `POST /api/v1/runner-join-tokens` | — | `runners join-token` | — | gap: no MCP tool makes join tokens yet |
| `POST /api/v1/runner-join-tokens/{id}/revoke` | — | `runners revoke-token` | — | gap: no MCP tool revokes join tokens yet |
| `GET /api/v1/runner-pools` | `list_config (kind runner-pools)` | `runner-pools list` | `launchway_runner_pool` | equal |
| `GET /api/v1/runners` | — | `runners list` | — | gap: no MCP tool lists runners yet |
| `POST /api/v1/runners/{id}/revoke` | — | `runners revoke` | — | gap: no MCP tool revokes runners yet |
| `POST /api/v1/runners/{id}/rotate` | — | `runners rotate` | — | gap: no MCP tool rotates runner keys yet |
| `GET /api/v1/runs` | `list_runs` | `runs list` | — | equal |
| `GET /api/v1/runs/{id}` | `get_run` | `runs show` | — | equal |
| `GET /api/v1/runs/{id}/artifacts` | `list_artifacts`, `read_artifact_file` | — | — | gap: launchway has no artifacts command yet |
| `DELETE /api/v1/runs/{id}/artifacts/{artifact}` | — | — | — | gap: deleting an artifact is UI only so far |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/archive` | — | — | — | gap: archive download is a browser download; no MCP tool or CLI yet |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/file` | `list_artifacts`, `read_artifact_file` | — | — | gap: launchway has no artifacts command yet |
| `POST /api/v1/runs/{id}/cancel` | `cancel_run` | `runs cancel` | — | equal |
| `GET /api/v1/runs/{id}/coverage` | — | `runs coverage` | — | gap: no MCP tool reads coverage yet |
| `GET /api/v1/runs/{id}/coverage/files` | — | `runs coverage` | — | gap: no MCP tool reads coverage yet |
| `POST /api/v1/runs/{id}/deployments/{dep}/abort` | `control_rollout` | — | — | gap: launchway has no rollout commands yet |
| `POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw` | `control_rollout` | — | — | gap: withdrawing a queued rollout command is UI only so far |
| `GET /api/v1/runs/{id}/deployments/{dep}/controls` | `get_deployments` | — | — | gap: launchway has no rollout commands yet |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote` | `control_rollout` | — | — | gap: launchway has no rollout commands yet |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` | `control_rollout` | — | — | gap: launchway has no rollout commands yet |
| `POST /api/v1/runs/{id}/deployments/{dep}/retry` | `control_rollout` | — | — | gap: launchway has no rollout commands yet |
| `GET /api/v1/runs/{id}/events` | `wait_for_run` | `runs logs` | — | equal |
| `GET /api/v1/runs/{id}/findings` | `get_security_findings` | — | — | gap: launchway has no findings command yet |
| `GET /api/v1/runs/{id}/images` | `list_images` | — | — | gap: launchway has no images command yet |
| `GET /api/v1/runs/{id}/pipeline-file` | `get_run` | — | — | gap: the pipeline text a run used is shown in the UI; get_run has the steps, the CLI has no command for the text |
| `GET /api/v1/runs/{id}/policy` | — | `policy run` | — | gap: no MCP tool shows a run's policy decisions yet |
| `POST /api/v1/runs/{id}/queue` | `queue_run` | `runs queue` | `not needed by terraform` | equal |
| `GET /api/v1/runs/{id}/reports` | — | `runs reports` | — | gap: no MCP tool lists hosted reports yet |
| `GET /api/v1/runs/{id}/rerun` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
| `POST /api/v1/runs/{id}/rerun` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
| `POST /api/v1/runs/{id}/resume` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
| `POST /api/v1/runs/{id}/review` | `review_rollback` | `deployments review` | — | equal |
| `POST /api/v1/runs/{id}/steps/{step}/approve` | `decide_approval` | — | — | gap: launchway has no approvals command yet |
| `POST /api/v1/runs/{id}/steps/{step}/explain` | — | — | — | gap: AI explanations of a step are asked in the UI; no MCP tool or CLI yet |
| `GET /api/v1/runs/{id}/steps/{step}/explanations` | — | — | — | gap: AI explanations of a step are read in the UI; no MCP tool or CLI yet |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `get_step_log` | `runs logs` | — | equal |
| `POST /api/v1/runs/{id}/steps/{step}/reject` | `decide_approval` | — | — | gap: launchway has no approvals command yet |
| `GET /api/v1/runs/{id}/steps/{step}/verify/controls` | `get_verification` | `verifications show` | — | equal |
| `POST /api/v1/runs/{id}/steps/{step}/verify/override` | `mark_verification` | `verifications mark` | — | equal |
| `GET /api/v1/runs/{id}/tests` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/runs/{id}/tests/cases` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/runs/{id}/tests/cases/{case}` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/runs/{id}/tests/cases/{case}/attachments/{n}` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/runs/{id}/tests/compare` | `get_test_results` | `runs tests` | — | equal |
| `POST /api/v1/runs/{id}/trust` | `trust_run` | `runs trust` | `not needed by terraform` | equal |
| `GET /api/v1/runs/{id}/variables` | `get_run_variables` | `variables run` | `not needed by terraform` | equal |
| `GET /api/v1/runs/{id}/verifications` | `get_verification` | `verifications show` | — | equal |
| `GET /api/v1/settings/approvals` | — | — | — | exempt: a read of one installation setting (approvals.allow_self), which get_config and the installation configuration kind give in full; the page needs it as a signed-in person, not an administrator |
| `GET /api/v1/setup` | — | `users setup-code` | — | exempt: the first-run page asks it before anyone can sign in; the console command issues the code |
| `GET /api/v1/setup-guide` | — | `doctor security` | — | exempt: the first-run guide is a walk-through in the browser; its facts are the same ones the other interfaces already read (runners, hosting, connections, projects) and the security checklist is launchway doctor security |
| `POST /api/v1/setup/begin` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `POST /api/v1/setup/complete` | — | — | — | exempt: sign-in, first-run setup and one-time links: credential flows done in a browser; the console covers them as launchway users |
| `GET /api/v1/signin/test` | — | — | — | exempt: the result of a provider test a person is doing in the browser; the same record is in the audit trail, which every interface reads |
| `POST /api/v1/signin/test` | — | — | — | exempt: testing an identity provider means a person completing the provider's own sign-in page in a second browser window; an API token or agent cannot and must not do that |
| `GET /api/v1/ssh` | `get_ssh_info` | `keys host` | — | equal |
| `GET /api/v1/step-resources` | `get_step_resources` | `step-resources get` | `launchway_step_resources` | equal |
| `GET /api/v1/step-resources/defaults/{kind}` | `get_step_resources` | `step-resources get` | `launchway_step_resources` | equal |
| `PUT /api/v1/step-resources/defaults/{kind}` | `set_step_resource_default` | `step-resources set-default` | `launchway_step_resources` | equal |
| `GET /api/v1/step-resources/limits` | `get_step_resources` | `step-resources get` | `launchway_step_resources` | equal |
| `PUT /api/v1/step-resources/limits` | `set_step_resource_limits` | `step-resources set-limits` | `launchway_step_resources` | equal |
| `GET /api/v1/stream` | — | — | — | exempt: live updates for the browser (server-sent events); agents and the CLI poll or wait_for_run |
| `GET /api/v1/tokens` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `POST /api/v1/tokens` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `GET /api/v1/tokens/limits` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `DELETE /api/v1/tokens/{id}` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `GET /api/v1/user` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `POST /api/v1/user/repos` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/user/ssh-keys` | `list_ssh_keys` | `keys list` | — | equal |
| `POST /api/v1/user/ssh-keys` | `add_ssh_key` | `keys add` | — | equal |
| `DELETE /api/v1/user/ssh-keys/{id}` | `delete_ssh_key` | `keys delete` | — | equal |
| `GET /api/v1/users` | `list_users` | `users list` | — | equal |
| `POST /api/v1/users` | — | — | — | exempt: sign-in and account recovery are done in the browser or at the server console, by the person concerned or an administrator at the keyboard; an API token or agent must not be able to create or recover people |
| `DELETE /api/v1/users/{name}` | — | — | — | exempt: sign-in and account recovery are done in the browser or at the server console, by the person concerned or an administrator at the keyboard; an API token or agent must not be able to create or recover people |
| `POST /api/v1/users/{name}` | — | — | — | exempt: sign-in and account recovery are done in the browser or at the server console, by the person concerned or an administrator at the keyboard; an API token or agent must not be able to create or recover people |
| `POST /api/v1/users/{name}/reset` | — | `users reset` | — | exempt: sign-in and account recovery are done in the browser or at the server console, by the person concerned or an administrator at the keyboard; an API token or agent must not be able to create or recover people |
| `POST /api/v1/users/{name}/sign-out` | — | — | — | exempt: sign-in and account recovery are done in the browser or at the server console, by the person concerned or an administrator at the keyboard; an API token or agent must not be able to create or recover people |
| `POST /api/v1/validate` | `validate_pipeline` | `validate` | — | equal |
| `GET /api/v1/variables` | `list_variables` | `variables list` | `planned: data source launchway_variables` | equal |
| `DELETE /api/v1/variables/{name}` | `delete_variable` | `variables delete` | `planned: resource launchway_variable` | equal |
| `PUT /api/v1/variables/{name}` | `set_variable` | `variables set` | `planned: resource launchway_variable` | equal |
| `DELETE /api/v1/variables/{name}/lock` | `unlock_variable` | `variables unlock` | `planned: resource launchway_variable (lock block)` | equal |
| `PUT /api/v1/variables/{name}/lock` | `lock_variable` | `variables lock` | `planned: resource launchway_variable (lock block)` | equal |
| `GET /api/v1/verifications` | `get_verification` | `verifications list` | — | equal |
| `GET /api/v1/waivers` | — | `waivers list` | — | gap: no MCP tool lists waivers yet |
| `GET /auth/callback` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /auth/login` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /auth/logout` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /auth/signin-test` | — | — | — | exempt: the address the identity provider sends the test window back to; it is part of sign-in, not an operation |
| `POST /git/{owner}/{name}/git-receive-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `POST /git/{owner}/{name}/git-upload-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /git/{owner}/{name}/info/refs` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /healthz` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /hosted/{link}/{path...}` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `DELETE /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /readyz` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /schema/pipeline.json` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /{owner}/{name}/git-receive-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `POST /{owner}/{name}/git-upload-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /{owner}/{name}/info/refs` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
