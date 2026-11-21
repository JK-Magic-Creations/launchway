# Encryption at rest

Launchway seals every secret it stores itself under a **root key that is held outside the database**,
so that a copy of the database, a backup or a volume snapshot alone reveals none of them. This page
says what is protected, what is not yet, how to turn it on, how to back the key up, and how to rotate
or replace it. It is for operators.

## What is protected, and what is not yet

| Protected now | How |
|---|---|
| The signing and sealing keys every server shares (sign-in state, links to hosted reports, Git run tokens) | Sealed in the `meta` table |
| The shared runner token | Sealed in the `meta` table. The server must hand it to its own runner and to the data directory's `runner-token` file, so it cannot be a one-way hash (the file is `0600` and is not in the database) |
| The private host keys of Git over SSH | Sealed in `ssh_host_keys`, bound to the algorithm |
| Webhook secrets | Sealed in `repo_webhooks`, bound to the hook |
| Webhook addresses | Sealed in `repo_webhooks.url`, bound to the hook, because a chat service's incoming webhook carries its token in the path. After they are added, the address is shown with the host only (`https://hooks.example/...`); to change it, enter the whole address again. The answer a receiver gave to a delivery is stored redacted (token-like strings and Authorization or Cookie values removed, and the address's own path) |
| Addresses with a password or token in them (mirrors, webhooks, connections) | Refused when written, so none is stored; `launchway doctor encryption` counts any that an older release stored |
| Sessions, API tokens, runner join and access tokens | Already stored only as SHA-256 hashes of 256 random bits, which cannot be reversed even with every key |

| **Not yet encrypted by Launchway** | Until the next slice, do this |
|---|---|
| Artifacts, test attachments and reports | Encrypt the volume or the database, and the S3 bucket (server-side encryption) |
| Logs and run records | The same |
| Build caches | The same. The cached archives are not encrypted, and cache entries are not signed; see [build caches](build-caches.md#what-a-cache-entry-is-not-yet) |
| Hosted Git repository packs | The same |
| Run metadata, project names, states, counts, the audit trail | These are searched, so they stay readable; use database and volume encryption |

The next slice adds per-object encryption of artifacts, logs, caches and Git packs under the same
keys. Until then, **encrypt the database volume and the artifact store as well**: Launchway cannot see
whether you have. A dump of the database today reveals build outputs and source history, though not
the secrets above.

`launchway doctor encryption` and **Settings, Security** print this state in plain words, including
"a database dump alone cannot reveal secrets: yes or no".

## How it works

```text
root key           held OUTSIDE the database by a holder (key file, environment, passphrase, Vault/OpenBao transit)
  wraps
key-encryption keys   versioned, stored wrapped in encryption_keks
  wrap
data keys             per purpose (and per organization where a row belongs to one), versioned, in encryption_deks
  seal
values                AES-256-GCM
```

A sealed value looks like `lwe1.` and a base64url string. After the prefix: one byte for the format
version, one for the algorithm (1 is AES-256-GCM), four for the **data key version**, a 12-byte random
nonce, then the ciphertext and its tag. AES-256-GCM is used because the Go standard library provides it
and it is in the FIPS 140-3 validated module; the algorithm byte is how another is added later. Nonces
are random; the few secrets sealed here are far below the count where that matters, and a data key
rotation starts a new count.

Every sealed value is bound, by **authenticated data**, to the installation id, its purpose, its
organization, and the table, column and row it is stored in. A value copied to another row, column,
table or organization does not open; neither does one made for another installation. A test pins each.

There is one place that seals and opens (`internal/sealing`), and a list of the columns that hold
secrets. A test walks the database schema and fails on a column that looks like it holds a secret and
is neither on that list nor explained; another fails on code that writes a listed column without
sealing; the canary test makes every kind of secret with a recognizable value, dumps the whole database
(the SQLite file and its log, or every text and binary column on PostgreSQL) and fails if the value
appears in any encoding.

## Turn it on

**New installation.** It is on by default. With no setting, a new installation generates a key file in
its data directory (`root.key`, mode `0600`) at its first start and says where, in the log. For
production, move it off that volume and point `encryption.key_file` at it. With the Helm chart, the
chart generates the key in a Kubernetes Secret (see below).

**Existing installation.** It keeps starting exactly as before, **not encrypted at rest**, with a loud
warning at start, in `launchway doctor` and on Settings, Security. To turn encryption on:

```sh
launchway encryption init --key-file /etc/launchway/root.key    # makes the key; prints where to keep a copy
```

Set `encryption.key_file: /etc/launchway/root.key` (or `LAUNCHWAY_ROOT_KEY_FILE`) on every server and
restart them. On the first start with the key, the secrets already stored are **sealed in place**; the
servers say when they are done. Then check:

```sh
launchway doctor encryption
```

Git over SSH and webhook secrets **refuse to turn on until a root key exists**, and say how to create
one. A server that has no key never makes one by itself in an installation that already holds secrets.

A server of the release before this one cannot read values a newer server has sealed: sign-in state, links
to hosted reports and Git run tokens, SSH host keys, and webhook secrets and addresses. A rolling update
runs both at once, so **the servers do not seal what already exists while an older server is running**.
A server that finds secrets in the clear serves at once (it reads them as before), writes only sealed
values for anything new, and seals the rest once no server of an older release has been seen for 45
seconds (`LAUNCHWAY_SEAL_QUIET_SECONDS`; `0` seals at start). An older server is seen because it holds
database connections that do not name themselves; this build names its own (`application_name=launchway`).
No setting is needed for the rollout, and there is no window in which an older server meets a sealed value.
`launchway doctor encryption` says "waiting for older servers" while it waits. Where a connection proxy
hides the connection names the server says so and seals at start instead; then use
`server.strategy.rollingUpdate.maxSurge: 0` with `maxUnavailable: "100%"` for that one rollout.
Going back to the release before is clean until the sealing has finished, and needs the backup taken
before the upgrade afterwards: see [Upgrading](upgrading.md).

## Where the root key lives

| Holder | You install | What it protects against | Notes |
|---|---|---|---|
| **Key file** (default) | nothing | A stolen database, backup or snapshot | 32 random bytes in base64, mode `0600` (a mounted Secret's `0440` also passes). A file that other users can read is refused. In Kubernetes the key is a Secret: **only as strong as the cluster's own secret encryption** (etcd encryption at rest and who may read Secrets), which Launchway cannot see. It defeats a stolen database dump, not a cluster administrator |
| **Environment** (`LAUNCHWAY_ROOT_KEY`) | nothing | The same | For containers and tests. The environment is visible to whoever can inspect the process |
| **Passphrase keystore** | nothing | The same, and the key is not stored in the clear on disk | `launchway encryption init --passphrase`. The server needs the passphrase at every start: from `encryption.passphrase_file`, or asked on a terminal. Argon2id (64 MiB, 3 passes) turns the passphrase into a key that unlocks a random root key kept in the file, so the passphrase can change without re-keying. For one server; unattended restarts need the passphrase file |
| **Vault or OpenBao transit** | an existing Vault or OpenBao | The root key never leaves the service; every unwrap is logged there; revoking access makes every secret unreadable at once | Uses the vault client and login methods of the secret stores (token variable, token file, or Kubernetes auth). The server needs `encrypt` and `decrypt` on one transit key only |

A cloud key service (AWS KMS, Google Cloud KMS, Azure Key Vault), PKCS#11 and a TPM are not built yet.
Each is one more holder: the interface is two operations (wrap a 32-byte key under additional
authenticated data, unwrap it) and a status, and nothing else changes when one is added.

Configuration is under `encryption` in [the server configuration](../reference/server-config.md#encryption);
the chart's values are in [the Helm values](../reference/helm-values.md#encryption-at-rest). Exactly one
holder may be configured; two at once are refused.

## With the Helm chart

By default the chart's install hook Job (`launchway bootstrap-secrets`, a Helm pre-install and pre-upgrade
hook; Argo CD runs it as PreSync) creates the `<release>-launchway-root-key` Secret **once, only if it is
absent**, and never changes or deletes it. Nothing in the rendered manifests is key material, so
`helm template` is the same every time, under Helm and under GitOps, and the Secret is not owned by Helm:
`helm uninstall` and a GitOps prune keep it. The servers mount it and set `LAUNCHWAY_ENCRYPTION_REQUIRE`, so
a server refuses to start without it. The configuration file does not change, so the release before can
still read it. To bring your own key, make the Secret and name it in `encryption.keyFile.existingSecret`
(the Job then makes none); or use `encryption.holder: transit`. `encryption.holder: none` is an explicit
opt-out: it runs not encrypted, with the warning.

```sh
openssl rand -base64 32 > root.key; chmod 600 root.key
kubectl -n launchway create secret generic launchway-root-key --from-file=root.key
# keep a copy of root.key outside the cluster, then set encryption.keyFile.existingSecret: launchway-root-key
```

Second factors of built-in accounts follow the same rule: an authenticator secret is stored sealed, so a
second factor can only be set up where a root key is in use (a fresh install always has one). With no
root key an administrator, who must have a second factor, cannot be created until encryption is on, or
`auth.local.require_second_factor` is explicitly `off`. Recovery codes are stored as salted Argon2id hashes.

## When the key is missing or wrong

- **No key, new installation:** a key file is generated (above).
- **No key, existing installation:** it starts, not encrypted, with the warning. With `encryption.require: true`
  (the chart sets it), it refuses to start.
- **No key, encrypted database:** the server **refuses to start**, plainly: *"this database is encrypted at
  rest, but no root key is configured on this server..."*. A dump alone never gives a working server.
- **A wrong key:** the server **refuses to start** with *"the configured root key does not open this
  database's encryption keys... it never re-keys by itself"*. It never makes new keys beside existing
  ones, and never silently re-keys.
- An unusable key file (unreadable, wrong size, readable by every user) stops the start with the reason.

Every failed unsealing is in the audit trail (`encryption.unseal`, outcome `failed`).

## Back up the key, separately

**A database dump is useless without the root key. That is the point, and the risk: lose the key and
every sealed secret is gone, and neither you nor anyone else can recover it.**

- Keep a copy of the root key (or the keystore **and** its passphrase) somewhere other than the
  database's backups: a password manager, or a sealed printout in a safe, ideally held by more than one
  person. Never in the same place as the dump.
- **Do not let the key and the dump be stored together.** The generated key file (`<data>/root.key`) lies
  in the data directory, which holds the SQLite database and is what a file-level backup or a volume
  snapshot copies, together with any dump kept there: such a copy holds the key and the data, and the key
  protects nothing. When the key file is inside the data directory, the server says so at start, and
  `launchway doctor`, `launchway doctor encryption` and Settings, Security warn *"the root key and the dump
  are stored together"* until the key is moved: set `encryption.key_file` to a file elsewhere (a mounted
  secret). With the Helm chart the key is a Kubernetes Secret, not in the database or in a dump taken
  with `pg_dump`; keep it out of any backup tool that also copies the same Secrets as the database's volume.
- Back it up again whenever you replace it.
- A restore needs both: the dump and the key. A restore to a test environment needs the key too, which is
  also how a stolen production backup stays safe there.
- The pre-deploy dump (`pg_dump`, or `sqlite3 .backup`) is unchanged: it contains only ciphertext and
  wrapped keys for everything on the table above.

**Practice the restore.** `launchway doctor encryption --restore-drill` opens every sealed secret with
the configured key, reading only, and exits 1 if any does not open. Run it against a restored copy:

```sh
launchway doctor encryption --config restored.yaml --restore-drill   # dump + key: every secret opens
# the same without the key:  "this database is encrypted at rest, but no root key is configured"
```

## Rotate

```sh
launchway encryption rotate           # a new key-encryption key; every data key is re-wrapped under it
launchway encryption rotate --data    # also new data keys, and every stored value re-sealed under them
```

Both run online, from any machine that has the key: no server restarts, requests are not blocked, and
`/readyz` is not affected. Old key versions stay readable until nothing needs them. Each rotation is in
the audit trail (`encryption.kek_rotated`, `encryption.dek_rotated`). The doctor warns when the oldest key
is over a year old.

## Replace the root key

Replacing the key (a person who held it left, or you are moving to another holder):

1. Make the new key: `launchway encryption init --key-file /etc/launchway/root-new.key`.
2. Configure the **new** key as the root key and the old one beside it: `encryption.key_file` (new) and
   `encryption.previous_key_file` (old), on the machine you run the next step on. The doctor says
   "a previous root key is still needed".
3. `launchway encryption rewrap`. Each key-encryption key is re-wrapped by the new key with a
   compare-and-swap, so servers keep running and a stopped run is finished by the next one. No value and no
   data key is touched.
4. Restart the servers with the new key alone. Keep a copy of the new key.
5. Destroy the old key when no backup you may need to restore still depends on it: **a backup taken before
   the re-wrap needs the old key.**

The same procedure moves between holders: configure the new holder as the root key and the old as
`previous_key_file` (or `LAUNCHWAY_ROOT_KEY_PREVIOUS`).

## Several servers

Sealing what was stored in the clear is safe with servers starting together: one takes a lease and seals in
batches of 200; the others wait up to a minute and see the result. Every row is changed by a
compare-and-swap, so a server that dies half way hands the work on within 30 seconds and nothing is sealed
twice or damaged. Progress is in `launchway doctor encryption`, in **Settings, Security**, and in
`GET /api/v1/encryption` (also the MCP tool `get_encryption_status`). Once everything is sealed, a value
found in the clear in a sealed place is **refused**, not believed, so a downgrade cannot be planted.

On SQLite the pass zeroes freed content as it goes and empties the write-ahead log, so the old plaintext
does not linger in the file. PostgreSQL keeps old row versions until they are vacuumed: run `VACUUM FULL` on
`meta`, `ssh_host_keys` and `repo_webhooks` after sealing, and remember that backups and replicas made
**before** sealing still hold the old plaintext: rotate those secrets (a new runner token, a new webhook
secret) if that matters to you.

## Commands, API and settings

| | |
|---|---|
| `launchway encryption init` | Makes a key file, or with `--passphrase` a keystore. Never overwrites |
| `launchway encryption status` | The same report as the web page, asking a server (needs sign-in as an administrator) |
| `launchway encryption rotate [--data]` | Rotation, above |
| `launchway encryption rewrap` | Root key replacement, above |
| `launchway doctor encryption [--restore-drill] [--json]` | On the server host, reading only; exit 1 when a dump alone could reveal a secret |
| `GET /api/v1/encryption`, MCP `get_encryption_status` | The report, for administrators; never any key material |
| Settings, Security | The report on a web page |

The reference is [the command-line reference](../reference/cli.md#launchway-encryption), [server settings](../reference/server-config.md#encryption)
and [environment variables](../reference/environment-variables.md). The checklist is in [Hardening](../security/hardening.md).

## What this does not do

It does not protect against someone who controls a running server process, which holds working keys in
memory; against a cluster administrator when the key is a Kubernetes Secret; or against someone who can
write to the database and restart a server (the audit trail's tamper evidence is a separate piece of
work). It does not replace database and disk encryption, which stay recommended as defense in depth, and
which are the interim protection for what is not yet encrypted above. It is not a compliance claim.
