# Agents (MCP)

How AI agents use Launchway through the Model Context Protocol: tools, rights, limits and the audit trail.

AI agents and assistants use Launchway through the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP). Every server serves it at `/mcp`; there is nothing else to deploy. An agent connecting with
nothing but a token can find out how to write a valid pipeline, check it, try it, start runs, watch
them and find out why they failed: the server tells it how, in its instructions, its tool
descriptions, the pipeline reference and worked examples it serves, and two prompts
(`diagnose_failed_run`, `write_pipeline`).

**Connecting.** The **Connect an agent** page makes a token for the agent and shows the
configuration with this server's address filled in. Agents that connect over HTTP need the address
and a header:

```json
{ "mcpServers": { "launchway": { "type": "http", "url": "https://launchway.example.com/mcp",
    "headers": { "Authorization": "Bearer lw_…" } } } }
```

Agents that start MCP servers as commands run `launchway mcp`, which forwards every message to the
server with the token `launchway login` stored (or `LAUNCHWAY_SERVER` and `LAUNCHWAY_TOKEN`), so the
tools, rights and limits are the server's and nothing runs locally:

```json
{ "mcpServers": { "launchway": { "command": "launchway", "args": ["mcp"],
    "env": { "LAUNCHWAY_SERVER": "https://launchway.example.com", "LAUNCHWAY_TOKEN": "lw_…" } } } }
```

**Protocol.** Streamable HTTP, protocol revision 2026-07-28, and the earlier revisions 2025-11-25,
2025-06-18 and 2025-03-26 for clients that still begin with `initialize`. The server keeps no
sessions: every request stands alone, so behind a load balancer any server may answer any request,
and there is no `GET` stream. Built with the official Go SDK.

**Tools.** Reading: `list_projects`, `get_project` (branches, run inputs and the pipeline file),
`list_runs`, `get_run` (each step, and in words why the run waits or failed and what to call next),
`wait_for_run` (at most 10 minutes, with progress notifications), `get_step_log` (the last lines,
the first lines, or lines containing a text: at most 500 lines and 64 KiB), `get_test_results`,
`get_security_findings`, `list_artifacts`, `read_artifact_file` (at most 64 KiB at a time),
`list_images`, `get_deployments` (with which rollout commands are possible now), `get_verification`
(a run's verifications, each check's outcome and why), `list_approvals`,
`validate_pipeline` (the server's own validator, with line numbers and the text of each line),
`get_pipeline_reference`, `list_templates`, `get_step_resources` (the default CPU and memory of
steps, and the limits on what they may ask for), `list_caches` (a project's build caches against its
quota) and `get_cache_policy`. Changing, listed only to tokens that may change
something on some project: `start_run` (with inputs, or with `pipeline_text` as a draft run),
`cancel_run`, `retry_run` (resume, or re-run from a step), `decide_approval`, `control_rollout` and
`delete_cache` and `mark_verification` (passed or failed, with a reason, as on the run page). Listed only to an
administrator, and audited: `set_step_resource_limits`, `set_step_resource_default` and `set_cache_policy`.
Tools are marked read-only or destructive with the protocol's annotations. Lists page with a cursor.
The resources `launchway://docs/pipeline-reference`, `launchway://docs/agent-guide` and
`launchway://docs/examples/<name>` hold the same documentation.

**Rights and the audit trail.** An agent can do exactly what its token allows: each tool calls the
server's own HTTP API with the token, through the same checks as any other call. Every call that
changes something is in the audit trail with `via MCP, token "<name>", tool <tool>, client "<the
agent's name>"` at the start of its detail, allowed or refused. Browser session cookies are not
accepted at `/mcp`. Make agents their own tokens, limited to the projects and the role they need.

**Untrusted text.** Logs, test names and messages, commit messages, findings, file contents and
pipeline files are written by whoever can push, and an agent reads them. Launchway returns such text
only inside fields named `untrusted`, never in tool descriptions, instructions or prompts; it tells
agents, in its instructions, that such text is data and never instructions; it masks secrets
exactly as the API does (the runner masks them before logs are stored); it strips control
characters and bounds every field. It cannot stop an agent from being persuaded by what it reads:
that is the agent's job, and yours in choosing what its token may do. Give agents viewer tokens
unless they must act, and keep approvals for people (the person who started a run may not approve it).

**Limits.** Each token may make 120 requests a minute to `/mcp` and have 4 in progress at once, and
each server answers at most 32 MCP requests at once (all tokens together), so agents cannot crowd out
people; beyond them the server answers `429` with `Retry-After`. Each server counts on its own. A
request may be at most 1 MiB, a tool call takes at most a minute (`wait_for_run` as long as it was
asked, up to 10 minutes), and every result is bounded as above.

**Security of the endpoint.** A request from a web page (one with an `Origin` header) is accepted
only from this server's own address (as `auth.oidc.redirect_url` gives it), from this server on a
loopback address, or from an origin in `mcp.allowed_origins`; anything else gets `403`, which stops
other sites, and DNS rebinding, from driving a server. Without sign-in (`auth.mode: none`) there is
no token, and the server listens only on a loopback address unless told otherwise.

**OAuth.** The current MCP authorization rules let a server accept access tokens from an OAuth
authorization server. With `mcp.oauth.resource` set (it needs `auth.mode: oidc`), `/mcp` is a
resource server for the sign-in provider: it publishes OAuth 2.0 Protected Resource Metadata (RFC
9728) at `/.well-known/oauth-protected-resource/mcp` naming the provider, answers `401` with a
challenge pointing there, and accepts the provider's JWT access tokens (RFC 9068) whose audience is
the resource, signed with the provider's keys, unexpired, and with every scope in
`mcp.oauth.scopes`. The person's rights are those of their groups in the token, or of their latest
sign-in if the token has none. What is not supported: Launchway is not an authorization server
(clients register with, and get tokens from, the provider; whether that works without help depends
on the provider supporting client ID metadata documents or dynamic registration, and the `resource`
parameter or an audience mapping), opaque access tokens and token introspection, and the provider's
tokens anywhere but `/mcp`. Launchway API tokens work either way.

```yaml
mcp:
  enabled: true                 # the default; false turns /mcp off
  allowed_origins: []           # browser pages that may call /mcp, besides this server's own
  requests_per_minute: 120      # per token, on each server
  concurrent: 4                 # per token, on each server
  total: 32                     # all MCP requests at once, on each server
  oauth:                        # optional
    resource: https://launchway.example.com/mcp
    scopes: [launchway]
```

The endpoint is on by default because it gives no right that a token does not already have over the
API. With the Helm chart these are `config.mcp.*`, written into the configuration only when set.
