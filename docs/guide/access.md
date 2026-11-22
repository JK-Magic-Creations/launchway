# Sign-in and access

Sign-in with OpenID Connect, roles, sessions, API tokens and the Logbook. A step-by-step setup is in [Sign-in with OIDC and roles](../howto/oidc-and-roles.md).

Out of the box Launchway has no sign-in (`auth.mode: none`): everything is done as the configured
`user`, with full rights. That suits a server on your own machine and nothing else, so **a server
without sign-in refuses to start unless it listens only on a loopback address** (`127.0.0.1`, `::1`,
`localhost`). If something in front of it already decides who may reach it, say so with
`auth.allow_anonymous: true`. The UI shows "No sign-in" on every page while this is the case.

## Signing in with OpenID Connect

Any OpenID Connect provider works (Authentik, Keycloak, Okta, Entra ID, Dex and others), through
discovery, without anything specific to one of them:

```yaml
listen: 0.0.0.0:8080
server:
  trust_proxy: true          # only behind a reverse proxy; see below
auth:
  mode: oidc
  oidc:
    issuer: https://sso.example.com/realms/acme   # exactly as in the provider's discovery document
    client_id: launchway
    client_secret_env: LAUNCHWAY_OIDC_SECRET      # or client_secret_file: /run/secrets/oidc-secret
    redirect_url: https://launchway.example.com/auth/callback
    # scopes: [openid, profile, email, groups]    # the default
    # username_claim: preferred_username          # the default; then email, then sub
    # groups_claim: groups                        # the default
  idle_timeout: 8h        # the default: a session unused this long ends
  session_lifetime: 24h   # the default: every session ends this long after sign-in
  access:
    - { group: platform-admins, role: admin }
    - { group: payments,        role: operator, projects: ["payments-*"] }
    - { group: everyone,        role: viewer }
    - { user: dana,             role: operator, projects: [web] }
```

Register Launchway with the provider as a confidential web application using the authorization
code flow, with `redirect_url` as its redirect URI. Launchway sends PKCE (S256), `state` and
`nonce`, and checks the ID token's signature, issuer, audience, expiry and nonce. The client secret
is read from an environment variable or a file, never from the configuration file.

The provider must put the person's groups in the ID token, or in its userinfo response, under
`groups_claim`; most providers need a claim mapping for that. Entra ID sends group object IDs
rather than names (use the IDs in `access`), and leaves groups out for people in more than 200.
Choose a `username_claim` that people cannot change themselves at the provider: `user:` bindings,
and every record of who did what, use it.

The endpoints are `GET /auth/login?next=/path` (where to return afterwards; only paths on this
server are accepted), `GET /auth/callback`, `POST /auth/logout`, and `GET /api/v1/me`, which says
who you are and what you may do. `GET /api/v1/info` then reports only the version and that sign-in
is on.

## Roles

| Role | May |
|---|---|
| `viewer` | see a Flight (codebase) and its Sorties (runs), logs, events, pipeline files and approvals; see the Stations (runners); use the Playground; open the pipeline editor |
| `operator` | everything a viewer may, and start, cancel, resume and re-run runs, approve and reject approvals, save pipelines, and start draft runs if `drafts.allow` is `operator` |
| `admin` | everything an operator may, read the Logbook, start draft runs if `drafts.allow` is `admin`, save a pipeline that has problems, and waive a policy's rule if `policy.waivers.allow` is `admin` (the default; see [Policies](policies.md)) |

Each entry in `access` grants a role to one `group` (from `groups_claim`) or one `user` (from
`username_claim`), on the Flights matching one of `projects`, or on every Flight if there are
none. Patterns use `*` (any characters but `/`), `?` (one character) and `[...]`. Where several entries
match, the highest role wins. Someone matching none can sign in but sees nothing. The Logbook
needs `admin` from an entry without `projects`.

An entry may also list `environments` (patterns, as for Flights). It then grants its role on
deployments (the commands of [Controlling a rollout](deployments.md#controlling-a-rollout-promote-abort-retry),
and approvals that name a deployment) to those Airfields (environments) only; on its Flights otherwise it
grants as usual. An entry without `environments` covers every Airfield. An API token limited to
a role or Flights never grants more there than its maker has:

```yaml
auth:
  access:
    - { group: developers, role: operator, projects: [shop], environments: [staging] }
    - { group: sre, role: operator, environments: [prod] }
```

Rights are worked out on every request from the configuration and the groups the provider gave at
the person's latest sign-in, and checked by the server whatever the UI shows. A Sortie, Flight or
approval you may not see answers `404`, exactly as if it did not exist; one you may see but not
change answers `403`. Lists and the live stream contain only what you may see.

## Sessions

Signing in sets a session cookie (`HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` when the
browser uses HTTPS). The session is a random 256-bit value; the database keeps only its SHA-256, so
every server accepts it and a copy of the database does not contain it. Signing out ends it on
every server, and live streams it opened close within 15 seconds. What the UI fetches by itself,
such as refreshing a list, does not count as activity for `idle_timeout`.

### Your account in one answer

`GET /api/v1/account/summary` (`launchway account show`, the `get_account_summary` tool) gives your profile, your SSH
keys, your API tokens with their limits and expiry, the state of your second factor, the browsers you are signed in
on, and a `features` map of which optional areas this installation has, so that a page links only to what exists.
`launchway account sessions` lists your browsers by an id that cannot be used to sign in; `launchway account revoke
ID` ends one, `account revoke --others` ends all but the one you are using (called with a token, which has no session
of its own, all of them). Both are recorded in the Logbook.

A person's **public profile** (`launchway account profile NAME`) shows their name and what they did lately on
repositories and Flights *you* may read. It never holds their address, groups, roles, tokens or sign-in times, and a
private repository you cannot read adds nothing, not even a count. `launchway squadrons team NAME` shows a Squadron's
members by role, the repositories behind its Flights and its Flights, to those who may see at least one of them.

## API tokens

Scripts and the [command line](commands.md) use API tokens: `Authorization: Bearer lw_...`. Make one
on the **API tokens** page with a name and an expiry. Every token expires: by default after 90
days, at most after `auth.tokens.max_days` (default 365, up to 3650). It is shown once; only its
SHA-256 is stored. A token acts as you, with your rights as of your latest sign-in, and can be
revoked at any time. It may be limited further:

- to a **role** (viewer, operator or admin), no higher than the highest you have: a viewer token
  of an admin reads and changes nothing;
- to **Flights**, by names or patterns such as `pay-*`: elsewhere it is as if the Flights did not
  exist. A token limited to Flights is never an admin of every Flight, so it cannot read the
  audit trail;
- to **repositories** hosted here (`acme/shop`, or `acme/*`) with `read` or `write` access: such a token
  can do nothing else;
- to **source addresses** (CIDR ranges): a request from elsewhere is refused and recorded.

See [Credentials for machines](credentials-for-machines.md) for the rules, for service accounts that
own tokens for automation, and for which credential suits which job. A token is named in the Logbook
(`token_id`, `token_name`) on every request it authenticates.

The page shows when and from which address each token was last used. Admins of every Flight also
see everyone's tokens there and may revoke any (`GET /api/v1/admin/tokens`,
`DELETE /api/v1/admin/tokens/{id}`), for example one that leaked. Tokens can only be made from a
signed-in browser, not with another token. Over the API: `GET /api/v1/tokens`,
`POST /api/v1/tokens` (`{"name": "...", "expires_in_days": 30, "role": "viewer", "Flights":
["pay-*"]}`), `DELETE /api/v1/tokens/{id}`, and `GET /api/v1/tokens/limits` for the longest a token
may last and the highest role you may give it.

A token that is refused (unknown, revoked, expired, or not allowed by its limits) is recorded in
The Logbook as `token.use`. An address that fails to authenticate 20 times within a minute
(with tokens or webhook secrets) is refused with `429` and `Retry-After` for the rest of that
minute; each server counts on its own and remembers at most 10,000 addresses. Behind a reverse
proxy, the address is the one the proxy saw only from a peer in `server.trusted_proxies` (see below); otherwise
`X-Forwarded-For` is ignored, so a client cannot dodge the limit by making addresses up.

A limited token works only on servers that know limits: an older server, during a Return to base (rollback),
refuses it rather than grant its owner's full rights. Tokens without limits work on both.

Launchway cannot tell when someone leaves your organization: their tokens keep the groups of their
last sign-in until they expire. Remove their tokens when they leave, with
`DELETE FROM api_tokens WHERE username = '...'` in the database.

## Requests from other sites

A request that changes something and is authenticated by the session cookie must carry the header
`X-Launchway-CSRF: 1`, and if it has an `Origin` header, it must be this server; otherwise it is
refused with `403`. Browsers do not let another site add that header, so no web page can act with
a visitor's session. The UI sends it; scripts using a token need not. Without sign-in the header is
required all the same, so a web page cannot drive a Launchway on `localhost`. Launchway's pages
cannot be shown in a frame on another site.

## Behind a reverse proxy

Launchway believes the headers a proxy adds (`X-Forwarded-Proto`, to mark cookies `Secure`, and the
client's address) only from a peer you name. List your proxies' addresses:

```yaml
server:
  trusted_proxies: [10.0.0.0/8]       # the proxies in front: ranges (CIDR) or single addresses
  client_address_header: X-Real-IP    # optional: the one-address header your edge sets;
                                      # empty reads X-Forwarded-For
```

The rule: a request whose TCP peer is **in** `trusted_proxies` has its forwarding headers believed; a
request from **any other address** has them ignored, so a client that reaches Launchway directly
cannot forge its address or the scheme. With `X-Forwarded-For`, the address is the last entry that
is not itself one of your trusted proxies (a chain of your own proxies is skipped, and anything a
client wrote before them is never read); with `client_address_header`, the header must hold exactly one
address, or it is ignored. Without `trusted_proxies` nothing is believed, unless the older
`server.trust_proxy: true` is set: that believes the headers from every peer (the server logs a warning
at start), which is right only if clients cannot reach Launchway except through the proxy. Prefer
`trusted_proxies` (Helm: `server.trustedProxies` and `server.clientAddressHeader`); `launchway doctor security` reports "client addresses are believed from any peer" until you do. The client address is what the Logbook, the failed-sign-in limit and a token's
source-address limit use. The proxy must pass the `Host` header through unchanged, and must not buffer
`/api/v1/stream`. Cookies are also `Secure` whenever `redirect_url` starts with `https://`.

## The Logbook {#audit-trail}

Every call that changes something (starting, canceling and resuming runs, starting draft runs
(`run.draft`), saving pipelines (`pipeline.save`), approving and rejecting, making and revoking
tokens, re-runs (`run.rerun`), deployment commands (`deployment.*`) and Cargo (artifact) deletions
(`artifact.delete`)), allowed or refused, with sign-in on or off, every token that is refused (`token.use`), every
webhook call (`webhook.git`, `webhook.run`), allowed or refused, every waiver granted or revoked (`policy.waive`,
`policy.revoke_waiver`), allowed or refused, and every sign-in, failed sign-in and sign-out is
recorded with its time, who, their address, the action, Flight, target (run, step or token name),
outcome (`ok`, `denied` or `failed`) and a short detail. A draft run is recorded with the SHA-256
of its text and a save with its branch and commit; the pipeline text itself is never in the
trail, nor are secrets, tokens and session ids. Launchway only ever adds to it. Admins read it on the **Logbook** page (**Settings → Logbook**) or with
`GET /api/v1/audit?user=&project=&action=&from=&to=&before=&limit=` (times in Unix milliseconds;
`before` is the `next_before` of the previous page). It is kept until you remove old rows from the
`audit` table yourself.
