# Path for security reviewers

A reading order for people who must decide whether Launchway is safe enough for their
organisation, and on what terms.

1. [Trust model and threats](../security/trust-model.md): boundaries, what each part holds, and
   the current limits. Read the limits section in full.
2. [Architecture](../start/architecture.md): how runs, runners and deployments work.
3. [Secrets](../guide/secrets.md): references, runner-side lookup, masking, allow-lists.
4. [Sign-in and access](../guide/access.md) and [Roles and permissions](../reference/roles.md).
5. [Supply chain](../security/supply-chain.md): pinning, SBOMs, provenance, signing, scans.
6. [Policies](../guide/policies.md) and the [rule catalogue](../reference/policy-rules.md).
7. [Controls and compliance](../security/controls-and-compliance.md): what exists, what is
   planned, how to present it to an assessor.
8. [Hardening checklist](../security/hardening.md).

The code is open: access decisions are made in one place in `internal/api`, and a test checks
that every route is in the permission table. Report problems privately:
[Reporting a vulnerability](../security/reporting.md).
