# The built-in Depot (registry) {#the-built-in-registry}

Store container images and Helm charts in Launchway itself, so a team needs no Depot of its own.

In the web UI the Depot is an area, at `/depot`, with three tabs: Images (what your pipelines built), Components (what is inside them) and the built-in store. The area is there even when the store is switched off; its tab then says so and that an administrator turns it on with `registry.enabled`. Addresses under the old name, `/images`, still lead here.

The Depot speaks the OCI Distribution API under `/v2/` on the same address as the web UI and the API, so
`docker`, `podman`, `skopeo`, `helm` and every other OCI client work with no special setup. It keeps its
bytes in the [Cargo (artifact) store](../artifacts.md) (the database unless you chose a directory or S3) and
everything else in the database, so there is nothing else to run and any server of a group can answer any
request, even the next chunk of an upload that another server began.

> **What is tested here.** The Depot is tested with `podman` (pull and push), `skopeo` (copy in and out), `helm` (push
> and pull as an OCI chart), the Go Depot client library (images, multi-platform indexes, referrers, cross-repository
> mounts) and tests of every endpoint class, on SQLite and PostgreSQL. The official OCI conformance suite
> could not be fetched when this was built and has **not** been run: it is the next step to claim conformance.

**Choose the store for the size you push.** On the database store (the default on PostgreSQL) a single
blob may be no larger than that store's largest object (2 GiB by default) and everything, Cargo and
Depot together, counts against its size limit; for larger images or a busy Depot set `artifacts.store` to
`s3` or a directory (see [where Cargo are kept](../artifacts.md)). The Depot is **on by default** wherever
the server has sign-in and a Cargo store; `registry.enabled: false` turns it off.

## Who may do what

Nothing is anonymous: every request needs credentials, and with sign-in off the Depot answers `503` rather than
be open to everyone who can reach the server.

A repository is named `PROJECT/NAME`, with the Flight (codebase) first: pushing to `registry.example.com/shop/api` makes
the repository `shop/api` in the Flight `shop`. Launchway's roles on the Flight decide:

| Role on the Flight | Depot |
|---|---|
| viewer | pull, list, inspect |
| operator | and push (and cancel an upload) |
| admin | and delete images and tags, set the rules, run retention |

A repository someone may not pull is answered exactly like one that does not exist. A token limited to some
Flights (`projects:` on the token) is limited in the Depot too.

**People** log in with an API token as the password; the user name is anything:

```text
docker login registry.example.com        # user: anything, password: a Launchway API token
podman push registry.example.com/shop/api:1.4.0
helm push shop-0.3.1.tgz oci://registry.example.com/shop/charts
```

Clients that follow the Bearer challenge ask `GET /v2/token`, which grants only what the caller may do, for five
minutes. Clients that send Basic credentials directly work too. Passwords and tokens travel over HTTPS only,
except on a loopback listener or with `registry.allow_insecure_http` for a private network.

**Pipelines** need no password. An image step that names no Depot (or `registry: launchway`) pushes here; the
server hands the step the address and a credential made for that one Sortie (run). The credential

- pulls the Flight's own repositories, and those of another Flight only if that Flight's policy lists the
  pulling Flight in `pull_from`;
- pushes only to repositories of its own Flight, and never for a pull-request or draft run;
- expires with the Sortie's time limit and stops working when the Sortie ends;
- is never stored: it is a signature over the Flight, the Sortie and an expiry.

```yaml
steps:
  image:
    kind: image
    context: .
    file: Dockerfile
    tags:
      - { repository: api, tag: { run: short_sha } }   # registry: launchway, and api means PROJECT/api
    sbom: true
    scan: { fail_on: high, mode: enforce }
```

Signatures, provenance and SBOM attestations the step writes are stored beside the image as cosign-style tags. The
scan result and each platform's SBOM are attached as OCI referrers (manifests whose `subject` is the image), so
`GET /v2/NAME/referrers/DIGEST` lists them and the Images tab shows them.

## Looking at it

The **Depot** tab of Images lists a Flight's repositories against its quota, and each repository's images:
tags, digest, size, platforms, who pushed it (and from which run), whether it is signed, has a bill of materials,
what the scan found, and a pull reference to copy. The command line and the MCP server show the same
(`launchway registry list`, `registry inspect`, `registry tags`).

## Keeping it in order

- **Quota.** A Flight may store 20 GiB by default (`registry.default_quota_gib`); a Wing may have one too.
  A distinct layer counts once however many images use it. A push over the quota is refused with a plain
  sentence that says what to do.
- **Immutable tags.** Tags matching a pattern (`v*`) cannot be pointed at another image or deleted once pushed. A pattern
  set at a wider level cannot be removed nearer.
- **Retention.** Rules per installation, Flight or repository: keep the newest N tagged images, keep tags matching
  patterns, delete untagged images older than D days. `launchway registry retention PROJECT` previews exactly what
  would go; `--run` deletes it. An image an Airfield (environment) runs (its version is the image's digest or tag) is never
  deleted, by retention or by hand. Retention and garbage collection also run on a schedule (`registry.maintain_every`).
- **Garbage collection.** Mark and sweep with a grace period (`registry.gc_grace`, an hour): blobs no manifest needs are
  removed only after nothing has touched them for that long, and each delete re-checks that, so a push in progress
  is never collected under its own feet. `launchway registry gc --dry-run` lists what would go. One server collects at a time.
- **Limits.** Largest blob and manifest, requests a minute per caller, repositories per Flight, tags per repository.
- **Wings.** Identical layers are stored once inside a Wing and never shared across two; a
  cross-repository mount works only inside one.

Deleting, policy changes, retention and collection are recorded in the Logbook (`registry.*`; see
[the audit actions](../reference/events.md)).

## What protects the bytes at rest

**Launchway does not encrypt the Depot's blobs itself in this release.** They are stored as pushed, like
Cargo. Encrypt the volume or database that holds the Cargo store, and turn on server-side encryption on an
S3 bucket. Per-object encryption under the installation's keys (see [Encryption at rest](encryption-at-rest.md))
is the next slice. `launchway doctor registry` and the Depot tab say the same.

## Not yet

Only OCI content is stored. npm, Maven, PyPI and Go module endpoints, the pull-through cache of public registries,
replication to other registries, promotion by digest without a copy and policy on push are later slices (see the
[roadmap](../roadmap-list.md)). Quotas, retention and immutable tags are set per level with
`launchway registry policy set` or the API, not yet in Base settings or Terraform. Webhooks for Depot
events are not wired: pushes and deletes are in the Logbook.

See also: [HTTP API](../reference/http-api.md#built-in-registry), [command line](../reference/cli.md),
[server settings](../reference/server-config.md#registry), [container images](images.md).
