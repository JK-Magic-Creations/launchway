# Built-in accounts

Launchway can keep its own accounts, so an installation needs no identity provider. They are the default for any
server that listens beyond its own machine (`auth.mode: local`). They can also stay on next to an identity
provider (`auth.local.enabled: true` with `auth.mode: oidc`), so switching providers can never lock everyone out.

## How people sign in

- **Password.** Stored as an Argon2id hash (64 MiB of memory, 3 passes, 2 lanes, a random salt per password), never
  the password. The hash is replaced by a stronger one at the next sign-in if the parameters are raised.
- **Password rules** follow current guidance: at least 12 characters, no required mix of cases or symbols, a small
  offline list of common and leaked passwords is refused (also with digits or symbols added), no forced
  rotation. A password may not contain the person's name or the name of the site.
- **Second factor.** An authenticator app (TOTP: six digits, 30 seconds) with ten single-use recovery codes. A code is
  never accepted twice. By default administrators must have one (`auth.local.require_second_factor: admins`; `all`
  and `off` are the other choices). Passkeys are planned and not built yet.
- **Throttle and lock.** After four wrong passwords the account locks for 30 seconds, doubling to 15 minutes, and the
  answer carries `Retry-After`. The lock is temporary on purpose: a permanent lock would let anyone lock an
  administrator out. A client address is also limited in how many attempts it may make.
- **Sessions.** A new random token at every sign-in, in an HttpOnly, SameSite cookie (Secure over HTTPS), ending after
  8 hours idle or 24 hours in all (`auth.idle_timeout`, `auth.session_lifetime`). Changing a password ends the other
  sessions; "sign out everywhere" ends all of them.
- **Audit.** Every sign-in, failure, lock, second-factor event, invitation, reset and role change is in the audit
  trail.

## The first administrator

A new installation has none. The server makes a one-time **setup code** (25 characters, valid 24 hours, works once)
and, until setup is done, every other API call answers `setup_required`. On Kubernetes the chart puts the code in the
Secret `<release>-setup-code`; on one machine it is printed once in the start-up message. Open the address, enter the
code, choose a name and password, add the authenticator and save the recovery codes. The first administrator is the
break-glass account.

Code expired or lost: `launchway users setup-code`, run on the server or in its pod, issues a new one (and cancels the
old one). It refuses once setup is complete.

## People, recovery and the console

An administrator invites a person by **one-time link** (valid 72 hours; there may be no e-mail, so the link is shown
once for the administrator to pass on). A person who is locked out gets a **reset link** the same way (24 hours), which
sets a new password and a new second factor. With no administrator able to sign in, the console does it:

```sh
launchway users list
launchway users reset ada      # prints a one-time reset link
launchway users unlock ada
launchway users setup-code     # only before the first administrator exists
```

These need only the database address in the server's bootstrap file, and are recorded in the audit trail as `console`.

The last enabled administrator cannot be removed, demoted or turned off. When an identity provider is the sign-in, the
last built-in administrator can be removed only after someone has signed in through the provider, and only with an
explicit confirmation.

## What is not built yet

Passkeys (WebAuthn), outgoing mail for invitations, and sealing of the authenticator secrets with the installation's
root key (they are stored in the database as they are; the database already needs protecting). See the
[roadmap](../roadmap-list.md).
