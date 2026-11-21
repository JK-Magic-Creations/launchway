# Controls and compliance

Which security controls Launchway has today, which are planned, and how they can support an
organization's own compliance work, for example under PCI DSS or the HIPAA Security Rule. For
security reviewers, auditors and decision makers.

## What compliance means here

Software on its own is not "PCI compliant" or "HIPAA compliant", and Launchway is not certified
against any standard. Compliance is assessed on the **organization** that runs a system, including
how it configures and operates it. A build and delivery platform that deploys into regulated
systems is usually in scope of that assessment even if it never sees regulated data, because it
can change what runs there.

What Launchway can do is provide **controls** and **evidence** that help an organization meet its
own obligations. This page lists them honestly: what exists on the main branch today, and what is
planned. It is not legal or assessor advice.

## Keep regulated data out of Launchway

The simplest way to reduce scope is to make sure card data, health information and other regulated
data never reach Launchway:

- Do not use production data in tests that Launchway runs.
- Do not print such data in step logs, test output or Cargo (artifacts). Only secret values from secret
  stores are masked automatically.
- Keep secrets in secret stores read by Stations (runners) in your own network; the server never sees them.

## Controls available today

| Control area | What Launchway does today | Where |
|---|---|---|
| Unique user identity | Every person signs in through your OpenID Connect provider; actions are recorded under their user name | [Sign-in and access](../guide/access.md) |
| Multi-factor authentication | Delegated to your identity provider: require it there | [OIDC and roles](../howto/oidc-and-roles.md) |
| Least privilege | Viewer, operator and admin roles per project, limited to Airfields (environments) where needed; API tokens limited to a role and projects | [Roles and permissions](../reference/roles.md) |
| Session management | Idle timeout and maximum session lifetime; sign-out ends the session on every server | [Sign-in and access](../guide/access.md#sessions) |
| Credential handling | Secrets resolved by Stations only, masked in logs, never stored by the server; sessions and tokens stored as hashes; tokens always expire | [Secrets](../guide/secrets.md) |
| Encryption of stored secrets | The server's own secrets (signing and sealing keys, the shared Station token, SSH host keys, webhook secrets) are sealed with AES-256-GCM under a root key held outside the database, each bound to its row; rotation and root key replacement online; a database dump alone is useless without the key. Cargo, logs, caches and Git packs are not yet encrypted by Launchway | [Encryption at rest](../guide/encryption-at-rest.md) |
| Change approval | Approval steps; with sign-in, the person who started a Sortie (run) cannot approve it by default | [Approvals and re-runs](../guide/approvals-and-reruns.md) |
| Separation of duties for production | Protected Airfields: the starter cannot promote or waive the gate; operator rights limited per Airfield | [Deployments](../guide/deployments.md#controlling-a-rollout-promote-abort-retry) |
| Testing before release | Policy rule `tests`: the steps before a deployment reported tests and none failed | [Policy rules](../reference/policy-rules.md) |
| Vulnerability management | Scanner findings (SARIF) per run with new and fixed; image scans with an enforced severity; policy rule `findings` | [Security findings](../guide/security-findings.md) |
| Component inventory | SBOMs and a search across projects for a component and version | [Security findings](../guide/security-findings.md) |
| Back-out | Return to base (rollback) steps revert the deployment's commit and wait until the previous version is healthy | [Deployments](../guide/deployments.md) |
| Integrity of what is deployed | Images pinned by digest, signed by the Station, with provenance; policy rule `deployed_images` | [Supply chain](supply-chain.md) |
| Station identity | Stations enrol with join tokens and hold their own key; leases are bound to the Station; pools limit a Station to the projects they serve; the Station's own `accept:` list limits what it will run; revocation and key rotation are audited. Stations still on the shared token have no identity | [Stations on other machines](../guide/runner-identity.md) |
| Post-deployment verification | Metrics and logs are checked after a deploy by built-in statistics; a failing verification can abort, roll back or hold a promotion. Optional AI models only explain, never turn a failure into a pass | [Verifying a deployment](../guide/verification.md) |
| Audit logging | Every change, sign-in, failed sign-in, refused token, webhook call and waiver, with who, when, from where and the outcome; only ever added to | [Sign-in and access](../guide/access.md#audit-trail) |
| Records of each change | Each Sortie's event log: commit, pipeline, inputs, approvals with comments, policy decisions with the facts judged, deployments and what was observed | [Events](../reference/events.md) |
| Exceptions | Policy waivers need a reason and an expiry, are recorded and shown where they changed a verdict | [Policies](../guide/policies.md) |

## Current limits that matter for an assessment

- Encryption at rest covers the server's own secrets, not yet Cargo, logs, build caches and Git packs: encrypt the database volume and the Cargo store. With no root key configured, an existing installation stores its own secrets in the clear (and says so).
- The database is the store of record. Anyone who can write to it can change the Logbook; the
  trail is append-only by design, not protected cryptographically. Protect the database and copy
  the trail to a log archive you control.
- There is no built-in retention control: runs, logs and audit records are kept until you remove
  them.
- Reads are not audited, only changes and sign-ins.
- MFA is whatever your identity provider enforces; Launchway does not check it.
- See the [trust model](trust-model.md#current-limits-and-what-is-changing) for Station limits.

## Planned controls

These are designed, not built. Status and order are on the [roadmap](../roadmap-list.md).

| Control area | Planned |
|---|---|
| Change records | One record per production deployment holding its reason, impact, approvals, tests, scans and back-out plan, exportable as evidence |
| Evidence export | A report of the controls above for a period, for an assessor |
| Separation of duties | Checks that the commit author, approver and deployer are different people |
| Multi-factor authentication | Require MFA (from the identity provider's claims) for sign-in and again for approvals, promotions and waivers; local accounts with MFA for installations without an identity provider |
| Access reviews | Periodic review of who has which role, with attestation and revocation |
| Tamper-evident audit | A hash-chained audit trail with a verification command, and streaming to a log archive |
| Retention | Retention per kind of data, legal hold, and deletion reports |
| Sensitive-data masking | Masking card-number patterns in logs on the Station, with a finding raised |

## How to use this page with an assessor

1. Decide whether Launchway is in scope, and keep regulated data out of it.
2. Configure the controls in the [hardening checklist](hardening.md).
3. Show the evidence: The Logbook, run event logs, policy decisions and waivers, the
   Airfields page history.
4. Record the [current limits](#current-limits-that-matter-for-an-assessment) and the
   compensating controls you use (database access control, log archiving, identity provider MFA).
