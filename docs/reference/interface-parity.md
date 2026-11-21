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
| `GET /api/v1/admin/tokens` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `DELETE /api/v1/admin/tokens/{id}` | — | — | — | gap: API tokens are made in the UI today; an MCP token must not make tokens; CLI and provider resource planned |
| `GET /api/v1/ai/models` | `list_config (kind models)` | `models` | `launchway_model` | equal |
| `GET /api/v1/approvals` | `list_approvals` | — | — | gap: launchway has no approvals command yet |
| `GET /api/v1/artifact-store` | — | `blobs status` | — | gap: blobs status reads the database directly; no MCP tool |
| `GET /api/v1/audit` | — | — | — | gap: the audit trail is read in the UI; no MCP tool or CLI command yet |
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
| `GET /api/v1/deployments` | `get_deployments` | — | — | gap: launchway has no deployments command yet |
| `GET /api/v1/environments` | `list_config (kind environments)` | `environments list` | `launchway_environment` | equal |
| `GET /api/v1/health-sources` | `list_config (kind health-sources)` | `health-sources` | `launchway_health_source` | equal |
| `GET /api/v1/images` | `list_images` | — | — | gap: launchway has no images command yet |
| `GET /api/v1/info` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /api/v1/launch-lines` | — | — | — | exempt: a read model for the Home page, worked out from a run's steps and deployments, which list_runs, get_run and get_deployments already give in full |
| `GET /api/v1/libraries` | `list_config (kind libraries)` | `libraries list` | `launchway_library` | equal |
| `GET /api/v1/libraries/{library}/templates` | `list_templates` | — | — | gap: launchway has no templates command yet |
| `GET /api/v1/me` | — | — | — | exempt: the caller's own identity: the token is the credential for MCP and the CLI |
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
| `POST /api/v1/projects/{name}/git-hook` | — | — | — | exempt: called by Git hosts and other systems with their own proof, not by people |
| `GET /api/v1/projects/{name}/inputs` | `get_project` | `projects get` | — | equal |
| `GET /api/v1/projects/{name}/lanes` | `list_lanes` | `runs lanes` | `not needed by terraform` | equal |
| `GET /api/v1/projects/{name}/pipeline` | `get_project` | `config get projects` | — | equal |
| `POST /api/v1/projects/{name}/pipeline` | — | — | — | gap: the pipeline editor commits to Git; agents and the CLI edit the file in the repository and check it with validate_pipeline and launchway validate |
| `GET /api/v1/projects/{name}/refs` | — | — | — | exempt: branch and tag picker of the UI; start_run and runs start take the ref as text |
| `POST /api/v1/projects/{name}/runs` | `start_run` | `runs start` | — | equal |
| `GET /api/v1/projects/{name}/schedules` | `get_project` | `projects get` | — | equal |
| `GET /api/v1/projects/{name}/security` | `get_security_findings` | — | — | gap: launchway has no findings command yet |
| `GET /api/v1/projects/{name}/tests` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/projects/{name}/tests/history` | `get_test_results` | `runs tests` | — | equal |
| `GET /api/v1/projects/{name}/variables` | `get_effective_variables` | `variables effective` | `planned: data source launchway_effective_variables` | equal |
| `POST /api/v1/projects/{name}/waivers` | — | `waivers grant` | — | gap: no MCP tool grants waivers yet |
| `POST /api/v1/projects/{name}/waivers/{waiver}/revoke` | — | `waivers revoke` | — | gap: no MCP tool revokes waivers yet |
| `POST /api/v1/projects/{name}/webhooks/{trigger}` | — | — | — | exempt: called by Git hosts and other systems with their own proof, not by people |
| `DELETE /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `PATCH /api/v1/repos/{owner}/{name}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repos/{owner}/{name}/branches/{branch}` | — | — | — | exempt: a compatibility call that tools written for other Git hosts send; the same operations are the repository tools and commands |
| `GET /api/v1/repositories` | `list_repositories` | `repos list` | `planned: launchway_repository` | equal |
| `POST /api/v1/repositories` | `create_repository` | `repos create` | `planned: launchway_repository` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}` | `delete_repository` | `repos delete` | `planned: launchway_repository` | equal |
| `GET /api/v1/repositories/{owner}/{name}` | `get_repository` | `repos get` | `planned: launchway_repository` | equal |
| `PATCH /api/v1/repositories/{owner}/{name}` | `update_repository` | `repos update` | `planned: launchway_repository` | equal |
| `GET /api/v1/repositories/{owner}/{name}/deploy-keys` | `list_deploy_keys` | `repos deploy-keys list` | `planned: launchway_deploy_key` | equal |
| `POST /api/v1/repositories/{owner}/{name}/deploy-keys` | `add_deploy_key` | `repos deploy-keys add` | `planned: launchway_deploy_key` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/deploy-keys/{id}` | `delete_deploy_key` | `repos deploy-keys delete` | `planned: launchway_deploy_key` | equal |
| `GET /api/v1/repositories/{owner}/{name}/hooks` | `list_webhooks` | `repos hooks list` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks` | `add_webhook` | `repos hooks add` | `planned: launchway_webhook` | equal |
| `DELETE /api/v1/repositories/{owner}/{name}/hooks/{id}` | `delete_webhook` | `repos hooks delete` | `planned: launchway_webhook` | equal |
| `PATCH /api/v1/repositories/{owner}/{name}/hooks/{id}` | `update_webhook` | `repos hooks update` | `planned: launchway_webhook` | equal |
| `GET /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries` | `list_webhook_deliveries` | `repos hooks deliveries` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries/{delivery}/redeliver` | `redeliver_webhook` | `repos hooks redeliver` | `planned: launchway_webhook` | equal |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/test` | `test_webhook` | `repos hooks test` | `planned: launchway_webhook` | equal |
| `GET /api/v1/repositories/{owner}/{name}/protected-branches` | `get_protected_branches` | `repos protected-branches` | `planned: launchway_branch_protection` | equal |
| `PUT /api/v1/repositories/{owner}/{name}/protected-branches` | `set_protected_branches` | `repos protected-branches` | `planned: launchway_branch_protection` | equal |
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
| `GET /api/v1/runs/{id}/reports` | — | `runs reports` | — | gap: no MCP tool lists hosted reports yet |
| `GET /api/v1/runs/{id}/rerun` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
| `POST /api/v1/runs/{id}/rerun` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
| `POST /api/v1/runs/{id}/resume` | `retry_run` | — | — | gap: launchway has no runs retry command yet |
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
| `GET /api/v1/runs/{id}/variables` | `get_run_variables` | `variables run` | `not needed by terraform` | equal |
| `GET /api/v1/runs/{id}/verifications` | `get_verification` | `verifications show` | — | equal |
| `GET /api/v1/settings/approvals` | — | — | — | exempt: a read of one installation setting (approvals.allow_self), which get_config and the installation configuration kind give in full; the page needs it as a signed-in person, not an administrator |
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
| `POST /git/{owner}/{name}/git-receive-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `POST /git/{owner}/{name}/git-upload-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /git/{owner}/{name}/info/refs` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /healthz` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /hosted/{link}/{path...}` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `DELETE /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /mcp` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `GET /readyz` | — | — | — | exempt: sign-in, health checks, the MCP endpoint itself, the runner enrolment handshake or hosted files: not an operation on configuration or runs |
| `POST /{owner}/{name}/git-receive-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `POST /{owner}/{name}/git-upload-pack` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
| `GET /{owner}/{name}/info/refs` | — | — | — | exempt: the Git smart-HTTP protocol: git clients speak it (clone, fetch, push), with their own token authentication |
