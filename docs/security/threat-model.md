# Threat model

What Launchway defends against, by part, using the STRIDE questions (spoofing, tampering,
repudiation, information disclosure, denial of service, elevation of privilege). It complements the
[trust model](trust-model.md), which lists what each part is trusted with. Current gaps are marked
and tracked in the security backlog.

## Assets

Source code and Git history; secrets and credentials that pipelines resolve; build outputs
(Cargo (artifacts), images, caches); the Logbook; the ability to run code on Stations (runners) and to deploy.

## Boundaries and threats

| Boundary | Threat | Defense today | Gap |
|---|---|---|---|
| Browser to server | forged sign-in, session theft, cross-site request, script injection | OIDC sign-in; sessions and API tokens stored only as SHA-256 hashes; cookies HttpOnly and SameSite=Lax, Secure over HTTPS; CSRF header plus Origin check; Content-Security-Policy allowing only own scripts; HSTS over HTTPS | no built-in rate limit or lockout on sign-in (the identity provider's job); no native TLS, a proxy terminates it |
| Station to server | a stolen or forged Station credential takes work | Station identities with per-runner keys and short-lived hashed access tokens; join tokens hashed, limited uses | the shared Station token is sealed in the database under the root key (in the clear only on a server with no root key); the `runner-token` file in the data directory holds it (mode `0600`) |
| Pipeline author to Station | a malicious pipeline reaches other teams' workloads, the cluster, the server's database | step pods: non-root, no privilege escalation, all capabilities dropped, RuntimeDefault seccomp, no service account token, optional network policy | egress is open unless the operator applies the policy; cloud metadata endpoints are not blocked by default |
| Pipeline to secrets | a step prints a secret | secrets are references only and never stored; resolved values are masked in logs and recorded by reference | masking is by exact value; transformed values (encoded, split) can escape it |
| Webhooks in | a forged call starts runs | HMAC signature or secret token checked in constant time; body size limit | none known |
| Server to the outside | server-side request forgery through webhooks and sources | egress allow-list for outbound calls | review of every new outbound call is manual |
| Data at rest | database or backup stolen | secret values are not stored; tokens are hashed; the server's own secrets (signing and sealing keys, the shared Station token, SSH host keys, webhook secrets) are sealed with AES-256-GCM under a root key held outside the database, each bound to its row ([Encryption at rest](../guide/encryption-at-rest.md)) | Cargo, logs, build caches and Git packs are not yet encrypted by Launchway (use volume or database encryption); a server with no root key stores its own secrets in the clear and says so; the key file in a Kubernetes Secret is only as strong as the cluster's secret encryption |
| Audit trail | an administrator edits history | append-only by convention | no hash chain, so tampering is not evident |
| Supply chain | a poisoned dependency or base image | pinned versions and digests, vulnerability and license gates, reproducible signed releases with SBOMs and provenance | base images are pinned by tag in the Dockerfile (digest pinning planned) |
| Repository (public) | a leaked secret in history | history scan with a shrink-only baseline | secret scanning and push protection are repository settings still to switch on |

## Not in scope

An attacker with write access to the database, with root on the server or with the identity
provider's administrator rights can do what those rights allow; Launchway limits the damage but
does not claim to prevent it.
