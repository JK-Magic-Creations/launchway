# Path for operators and platform engineers

A reading order for people who install Launchway and run it for others.

1. [Architecture](../start/architecture.md): the parts, high availability, runners, delivery.
2. Install: [one server](../howto/install-single-server.md), or
   [Kubernetes with several servers](../howto/install-kubernetes.md)
   ([EKS](../howto/install-eks.md), [GKE](../howto/install-gke.md)).
3. [Sign-in with OIDC and roles](../howto/oidc-and-roles.md): before anyone else can reach it.
4. [Hardening checklist](../security/hardening.md#for-operators).
5. [Connect a runner](../howto/connect-runner.md) and [Secret stores](../howto/secrets.md).
6. [Backup, restore, upgrade](../howto/backup-restore-upgrade.md) and
   [Monitor Launchway](../howto/monitoring.md).
7. For delivery: [Container images](../guide/images.md), [Deployments](../guide/deployments.md),
   [Policies](../guide/policies.md) and [Verifying a deployment](../guide/verification.md).
8. [Runners on other machines](../guide/runner-identity.md) (pools, join tokens and each runner's own
   limits), [Where artifacts are kept](../artifacts.md), and optionally [AI models](../guide/ai.md)
   and [Agents (MCP)](../guide/agents.md).

Keep at hand:

- [Server configuration](../reference/server-config.md), [Runner configuration](../reference/runner-config.md),
  [Helm values](../reference/helm-values.md).
- [Roles and permissions](../reference/roles.md), [HTTP API](../reference/http-api.md).
- [Troubleshooting](../howto/troubleshooting.md).

For a platform team: publish your build, scan and release steps as a
[shared library](../guide/libraries.md), and require them with the `required_templates`
[policy rule](../reference/policy-rules.md).
