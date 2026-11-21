# Reference

Exact descriptions of every file format, setting, command and API. The pipeline, configuration
and Helm values references are checked against the code by tests, so they cannot silently fall
behind it.

| Reference | What it lists |
|---|---|
| [Pipeline file](pipeline.md) | every key of `.launchway/pipeline.yaml` and of library templates |
| [Server configuration](server-config.md) | every setting of `launchway.yaml` |
| [Runner configuration](runner-config.md) | every setting of `runner.yaml` |
| [Helm values](helm-values.md) | every value of the Helm chart |
| [Command line](cli.md) | every command and flag of `launchway` |
| [HTTP API](http-api.md) | every route, who may call it, and what it returns |
| [Configuration objects](config-objects.md) | the kinds of configuration kept in the database, their settings, and how the API, CLI, MCP and Terraform name them |
| [Interface parity](interface-parity.md) | every API operation against its MCP tool, CLI command and Terraform resource |
| [Accessibility conformance](accessibility-conformance.md) | each WCAG 2.2 criterion: what is supported, partly supported or not, with remarks |
| [Archive format](archive-format.md) | the document that stores another server's issues and comments read-only in a repository |
| [Events and audit actions](events.md) | the run event log, the live stream and the audit trail |
| [Roles and permissions](roles.md) | what viewers, operators and admins may do |
| [Policy rules](policy-rules.md) | the policy rule catalogue |
| [Environment variables](environment-variables.md) | what Launchway reads, and what steps receive |
