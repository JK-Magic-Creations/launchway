# Publishing a release to an outside Git host (design, not built)

Status: designed, not built. A push mirror copies a hosted repository's refs, so the tag of a published release reaches the outside host. The release itself (its notes and files, the page people open there) is not created. This page says how it would be, and why it was not built with the cut-a-release work.

## What is wanted

After a release is published here, create the matching release on the outside host of a push mirror: same tag, same notes, same files. Off by default, per repository, audited, and failing soft: the Launchway release stands, the outside publish shows as failed with its reason and a retry.

## Design

- A small interface in `internal/release/outside`: `Publish(ctx, Target, Release) (Result, error)`, where `Target` is the mirror's address and credential reference and `Release` is the tag, title, notes, prerelease flag and the files (a name, size and an opener). It returns the outside address of the release. A second host is another implementation; nothing outside the package names a host.
- One first implementation for the most common public host's REST API: create a release for an existing tag (`POST /repos/{owner}/{repo}/releases`), then upload each file to the upload address the answer names. The address of the API is derived from the mirror's address (`https://api.<host>`, or a configured override for a self-hosted one); the request goes through the egress guard, so an internal address needs the same allowance a mirror needs.
- The credential is the mirror's own reference (`--credential-env` or `--credential-file`), resolved at the moment of the call, never stored with the result and never logged. A token that may push may not be allowed to make releases: the failure says so.
- Opt-in: a boolean `publish_releases` on the push mirror (API, CLI `repos mirror add --publish-releases`, MCP `set_mirror`, Terraform), default false, audited as `mirror.update`.
- State: a table `release_outside` (migration 55, additive): release id, mirror id, state (`pending`, `published`, `failed`), outside address, reason, attempts, times. A background worker takes `pending` rows after the tag has been mirrored (it waits for the mirror's next successful sync, so the tag exists there), retries with backoff, and records every attempt as `release.outside_publish` in the Logbook. The release page shows the state and a **Retry** button (`POST /api/v1/projects/{name}/releases/{version}/outside/{mirror}/retry`, with CLI, MCP and parity rows).
- Notes are Markdown written by people and are sent as they are; files are streamed from the release store, so large files are not held in memory. Withdrawing a release marks the outside release a draft, never deletes it.

## Why it was not built now

It needs a migration, a worker with retries, a mirror setting across API, CLI, MCP and Terraform, a retry action in every interface, a REST client with uploads, and a fake server for its tests: more than the small, safe addition the cut-a-release work allowed, and it must not be rushed because it sends the owner's files to a third party. The tag mirroring that exists already gives the outside host the tag and the source archive of that tag.
