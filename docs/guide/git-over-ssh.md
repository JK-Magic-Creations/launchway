# Git over SSH

Launchway can serve the repositories it hosts over SSH as well as HTTPS, with no separate SSH server to
run. People add their public keys, a repository can have deploy keys of its own, and `git clone`,
`git fetch` and `git push` work with `ssh://` or `git@host:owner/name.git` addresses. Pushes go through
exactly the same permission checks, branch protection, audit trail, events and pipeline triggers as
pushes over HTTPS: there is one decision path, and a push refused over one is refused over the other
with the same words.

SSH is **off until you set `forge.ssh.listen`** (see [server settings](../reference/server-config.md#forge)).
Git hosting itself (`forge.hosting`) must be on.

## What the server allows

The SSH server speaks Git and nothing else.

| Allowed | Refused |
|---|---|
| `git-upload-pack` (fetch, clone), `git-receive-pack` (push), `git-upload-archive` (`git archive --remote`) on a path `owner/name` | A shell, any other command, a pty, port forwarding, remote forwarding, agent forwarding, subsystems |
| Public keys: ed25519, ECDSA, RSA of at least 3072 bits; user certificates from a trusted authority (optional) | Passwords, keyboard-interactive, DSA keys, RSA keys under 3072 bits, SHA-1 signatures |
| Modern key exchange (including a post-quantum hybrid), ChaCha20-Poly1305 and AES-GCM | Old key exchanges, CBC ciphers |

Anything refused gets one plain line, for example:

```text
Launchway: "ls" is not available: this server runs only git-upload-pack, git-receive-pack and git-upload-archive; shell access is not provided
```

A bare `ssh -T git@host` is greeted by name and sent away:

```text
Hi ada! You are signed in to Launchway; shell access is not provided.
```

Git protocol version 2 is used for fetch and clone. A large push is streamed to storage, never held in
memory, and is capped by `forge.max_push_mib` like an HTTPS push.

## Add your key

In the web UI, open **Settings, SSH keys**, paste the public key (the line in your `.pub` file), give it a
name and, if you like, an expiry. From the command line:

```sh
launchway keys add --file ~/.ssh/id_ed25519.pub --name laptop
launchway keys list
```

Each key shows its **SHA256 fingerprint**, when it was last used and from which address. A key can belong
to **one owner**: you, or one repository as a deploy key. Adding a key that is already registered is
refused. A key with an expiry stops working at that time. Delete a key and it stops working at once.

A key is refused, with the reason, when it is a DSA key, an RSA key under 3072 bits, a private key (never
paste one), has options such as `command=`, or is a certificate. To make a good key:

```sh
ssh-keygen -t ed25519 -C "laptop"
```

## Clone addresses

The repository page shows the clone address with an **HTTPS / SSH** switch. The SSH choice appears only
when SSH is on, so a button never offers an address that does not work. The addresses look like
`ssh://git@git.example.com:2222/acme/shop.git`, or `git@git.example.com:acme/shop.git` when the port is 22.
The user name is always `git`; who you are comes from your key.

## Check the server's host key

The first time you connect, `ssh` shows the server's fingerprint and asks you to accept it. Compare it
with the **host key fingerprints** page (Settings, SSH keys, "Verify this server") or `launchway keys host`.
The server presents an ed25519 key, an ECDSA key and an RSA key, so older clients can connect too.

## Deploy keys

A deploy key opens **one repository** and is meant for a machine: a build server, a mirror, a deployment.
It is **read-only unless you allow writing**. Repository administrators manage them under
**Repository settings, Deploy keys**, or with:

```sh
launchway repos deploy-keys add acme/shop --file ci.pub --name ci
launchway repos deploy-keys add acme/shop --file release.pub --name release --write
launchway repos deploy-keys list acme/shop
```

A deploy key that tries to push without write access is told so: `this deploy key is read-only: it can
clone and fetch, not push`. Used on another repository it gets "repository not found". A deploy key that
may write is still not an administrator, so it cannot push straight to a protected branch.

## Protected branches

A repository's administrators can protect branches. A protected branch takes a direct push only from an
administrator of the repository and cannot be deleted by anyone; every branch refuses a push that is not a
fast-forward. Rules are branch names or patterns where `*` matches any run of characters except a slash:

```sh
launchway repos protected-branches acme/shop --set main,release/*
```

The rule is enforced inside the receive, before any ref moves, so it applies to HTTPS and SSH alike.
Status checks, approvals and merge rules arrive with pull requests.

## Certificates from your own authority

If your organization signs SSH user certificates, put the authority's public key in `forge.ssh.user_ca`
(one key per line). A certificate signed by it is accepted when it is a user certificate with at least one
principal, has no critical options and is valid now. **The first principal is the person's Launchway
name**, so sign certificates with the name people have in Launchway as the principal. Certificate
sign-in has no groups; the person gets the rights bound to the name.

## Several servers

Run as many Launchway servers as you like behind one address. **Host keys are made by the first server
that needs them and stored, sealed with AES-256-GCM, in the database**, so every server presents the same
keys and clients never see a warning when a different server answers. The sealing key is kept in its own
database row, shared by the servers; losing it would make the host keys unreadable, so back up the
database as a whole.

Behind a load balancer, give the Service a TCP port for SSH; see the
[Helm chart notes](../reference/helm-values.md#git-over-ssh) for a Gateway and a LoadBalancer example.

## Limits and safety

| Setting | Default | What it does |
|---|---|---|
| `forge.ssh.max_connections` | 256 | Connections in all |
| `forge.ssh.max_per_address` | 20 | Connections from one address |
| `forge.ssh.idle_timeout` | 10m | A connection that moves no bytes is closed |
| `forge.ssh.max_duration` | 6h | The longest any connection lasts |

A connection must finish its handshake and sign in within 20 seconds, may try up to 6 keys, and an
address with 10 failed sign-ins in a minute is refused for 5 minutes. Git itself is also capped by
`forge.max_concurrent` and a per-person share, the same caps as HTTPS.

## Checking it works

`launchway doctor ssh` connects to the address and reports whether the SSH port is reachable and which
host key it presents. In the Logbook, a push over SSH is `git.push` with the detail `ssh key SHA256:...`
and the refs it moved.
