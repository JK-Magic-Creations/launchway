# Credentials for machines

Automation should hold the least credential that does its job. Launchway gives you four building
blocks, and this page says which to use for which job.

| Building block | What it is | Limits |
|---|---|---|
| **API token** (a person's) | Made by a person in the browser ([Access](access.md)). Acts with that person's rights at the moment of use. | role, Flights, repositories, source addresses, expiry |
| **Service account's token** | The same token, but owned by a non-person account, made by an administrator. Survives people leaving. | the same, and never more than the account's role |
| **Sortie credential** | Made by the server for one Sortie (run) and one repository. You never make or see it. | read-only, one repository, 30 minutes |
| **Deploy key** | An SSH key for one repository ([Git over SSH](git-over-ssh.md)). | one repository, read-only unless write |

Every token always expires (at most `auth.tokens.max_days`, 365 by default).

## Limiting a token to repositories

A token may name hosted repositories (`acme/shop`, or `acme/*` for every repository of one owner)
and an access level, `read` or `write`. **Such a token can do nothing else**: it reaches those
repositories over HTTPS Git and the repository API, lists only those, and is refused everywhere
else (Flights, Sorties, the Depot (registry), the Logbook, tokens, people), whatever its owner may do.

The rules, exactly:

- `acme/shop` names one repository, and the token keeps meaning *that repository*: it follows a
  rename, and stops covering it when it is deleted (a repository made again under the name is not
  covered). `acme/*` is every repository whose owner is exactly `acme`, now and later; it never
  reaches `acme-other/x`.
- A repository limit cannot be combined with Flight patterns; make two tokens.
- Most restrictive wins: on a listed repository the token has the **lowest** of: your role there,
  the token's role limit (if any), and its access level (`read` is viewer, `write` is operator).
  A repository token never has administrator rights over a repository.
- A repository token cannot make tokens.

## Limiting a token to source addresses

`source_ranges` lists CIDR ranges (`203.0.113.0/24`, `2001:db8::/32`) or single addresses. A request
from anywhere else is refused with `403` and a sentence naming the ranges, and the refusal is
recorded in the Logbook under the token's name.

The address is the one Launchway sees. Behind a proxy that is only meaningful if you tell Launchway
which proxies to believe, with `server.trusted_proxies` (see [Access](access.md#behind-a-reverse-proxy)):
a forwarding header from any other peer is ignored, so it cannot be forged.

## Service accounts

A service account has a name, a role (`viewer` by default, the most any of its tokens can have), no
password and no sign-in. An administrator makes it and its tokens with the API or the CLI, so nobody
has to switch sign-in modes or issue automation tokens from a personal or break-glass account. The
tokens act as `service:<name>`, and every audit line says which token did what.

Who may make a service account's tokens: an administrator's **signed-in session**; or an
administrator's **API token**, only where `auth.tokens.service_accounts_from_tokens: true` (off by
default). A service account's own token, an agent's token, and a limited token never may. The rule
that a token cannot make another token still holds for people's tokens.

Switching a service account off, or deleting it, stops its tokens at once.

## Which credential for which job

**A mirror job that reads repositories.** A service account with role `viewer` and a read token for
exactly the repositories it mirrors, limited to the address the job runs from.

**A GitOps controller.** A service account with role `viewer` and a read token for the one
repository (or `owner/*`) it syncs, limited to the cluster's egress addresses. Give it the token as
the password of HTTPS Git (any user name).

**A build system elsewhere** that clones your code. A read token for the repositories it builds. If
it pushes results back (tags, a bump commit), make a second, write token for just that repository.

**A deploy step that writes back to a repository.** Nothing to make: the server mints a credential
for each deploy step, bound to that deployment ([GitOps write-back](gitops-write-back.md)).

**A pipeline on a repository this installation hosts.** Nothing to make either (below).

**A job that needs one repository over HTTPS, as a deploy key does over SSH.** A service account
token limited to that one repository with `read` (or `write`): that is the HTTPS equivalent of a
deploy key, so there is no second mechanism. Make it in one step:

```bash
launchway service-accounts create ci-shop --role viewer --description "build system for shop"
launchway service-accounts token ci-shop --name build --repo acme/shop --access read \
  --from 203.0.113.0/24 --days 90
```

## A pipeline on a repository hosted here

When a Flight's repository address is one of this installation's own repositories, nothing needs
configuring: no token, no environment variable on servers or Stations, no egress rule, no internal
address.

The rule: the address is the installation's own when it is, exactly, the address Launchway gives for
one of its repositories: `<public url>/git/owner/name.git`, the same without `/git` (the address
other Git hosts use), or the SSH address, each with or without `.git` or a trailing `/`, the scheme
and host compared without regard to case. A look-alike on another host is another repository.

- **Servers** read the repository from their own storage: no network, no credential.
- **Stations** are handed, when they lease a job, a read-only credential made for that repository and
  that run (30 minutes), and clone through the server they already talk to, at
  `<the server address the Station uses>/git/owner/name.git`. No new address or egress rule is
  needed on a Station that already reaches its server. The credential cannot push and cannot read
  any other repository.

Settings you already made (`GIT_CONFIG_*` environment variables on a Station) keep working: they are
added to, not replaced. Shared libraries hosted here are not yet carried the same way; those still
need the credentials they needed before.
