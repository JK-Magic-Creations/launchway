# Making a release

The short path is [Cut a release](cut-a-release.md): one button that makes the tag, attaches the built files and publishes.

A release is more than a tag with a zip. It is the thing that is versioned, cleared, promoted from Airfield to Airfield and reported on: a version, the commit and annotated tag it was made from, notes, Cargo (files with checksums), the image digests its Sortie built, and a record of who did each step. This page is about making one. [Promoting a release](promoting-releases.md) is about moving it through the Airfields, [Release checklists, windows and freezes](release-checklists.md) about what must be true first, and [Measuring delivery](measuring-delivery.md) about what it tells you afterwards.

You can do everything here from the web interface (Deployments, then Releases), the command line (`launchway release ...`), the HTTP API, or through an agent (the `*_release*` tools); they are the same operations with the same permissions.

## The life of a release

A release is in one of four states.

| State | What it means | What can still change |
|---|---|---|
| Draft | Made, not tagged. | Notes, title, files; it can be deleted. |
| Candidate | Offered for testing. It may be promoted to Airfields that are not protected. | Notes, title, files. |
| Published | Tagged and fixed. | Nothing. It can only be withdrawn. |
| Withdrawn | Published or candidate, then withdrawn with a reason. It stays in the record and is no longer promoted. | Nothing. |

**A published release never changes.** Its version, commit, tag, Cargo and image digests are fixed, assets are never replaced, and even an administrator cannot edit its notes. If something is wrong, withdraw it (with a reason) and make a new release: a correction is a new version. This is checked in the database, not only in the interface: every change is made only while the release is still a draft or candidate, in the same statement.

## Versions

Each Flight (codebase) says how its versions are written and ordered, in its release settings (`launchway release settings PROJECT --scheme ...`, admin on the Flight):

- `semver` (the default): Semantic Versioning 2.0.0. `1.4.0`, `2.0.0-rc.1`, `1.4.0+build.5`. Ordered by the rules of the specification: a pre-release comes before its release, `rc.2` before `rc.10`, and build metadata is ignored.
- `calver`: calendar versions that start with the year, such as `2026.11.21`, ordered number by number.
- `natural`: any text; runs of digits compare as numbers, so `r9` comes before `r10`.
- `created`: any text; the order is the order releases were made.

A new version must be **higher than the highest so far**, unless you say it may not be (`--allow-lower`, for a release from a maintenance branch; see below). The same version twice is never allowed. The scheme cannot be changed once the Flight has a candidate or published release, because the order of its versions would change under it. The tag is the version with the Flight's `tag_prefix` (`v` by default).

### Which version next

`launchway release next --project P` reads the commits and merged pull requests since the last release and proposes a version, with the reason for each change:

- a change marked breaking (`type!:` in a conventional commit, `BREAKING CHANGE:` in the body, or a label `breaking`) asks for a **major** version (before 1.0.0, the minor number);
- a feature (`feat:`, or a label `feature` or `enhancement`) asks for a **minor** version;
- anything else asks for a **patch**.

The highest wins. It is a proposal: a person confirms it or chooses another, and nothing is made by asking. With `--pre rc` it proposes the next pre-release (`1.5.0-rc.1`, then `-rc.2`). Where the Flight is hosted here, the labels of the merged pull requests are used; elsewhere the commit prefixes are.

## Making a draft

```
launchway release create --project shop --version 1.5.0 --generate-notes --run run_abc
```

The release is made from a branch, tag or commit (`--from`, default the default branch), or from an existing annotated tag (`--adopt v1.5.0`). `--run` names the Sortie that built it: its image digests are recorded, and they are what a promotion launches. The Sortie must have run at the release's commit, or the release is refused: a release carries what was built from its own commit.

### Notes

`--generate-notes` writes Markdown from the merged pull requests and commits since the previous release, grouped as breaking changes, security, features, fixes, dependencies and other changes, with links and the contributors. You edit them before publishing (`launchway release edit ... --notes-file`, or the editor on the release's page), and can write them again from the changes at any time while it is a draft. Where the work tracker is present, closed work items are the source instead; the code asks a small interface for them (`ReleaseNotesSource`) and falls back to pull requests and commits.

Notes are written by people. Launchway shows them through its Markdown renderer, which never interprets what is written as HTML.

`launchway release changelog PROJECT VERSION` offers the notes as a changelog entry in a **pull request**: it commits them at the top of the changelog file (`CHANGELOG.md` by default) on a new branch and, on a hosted repository, opens the pull request. It never pushes to the branch itself, so a protected branch stays protected.

### Cargo and images

```
launchway release assets add shop 1.5.0 --file ./shop-linux-amd64
launchway release assets add shop 1.5.0 --run run_abc --artifact art_1 --path dist/shop --images
```

A file is kept with the release with its SHA-256 (worked out as it is stored; give `X-Launchway-SHA256` to have it checked). Files copied from a Sortie's Cargo are checked against the checksum the Sortie recorded and are copied into the release's own storage, so they outlive the Cargo, which expires. `--images` records the Sortie's image digests and, where they exist, summaries of its SBOM and scans. Each asset shows its provenance: which Sortie built it, at which commit, and who added it. Downloads need a signed-in person who can read the Flight, are streamed, answer `Range` and are recorded in the Logbook.

## Publishing

`launchway release publish PROJECT VERSION` creates the **annotated tag** at the release's commit and publishes the release. If the tag already exists at that commit it is adopted; if it exists anywhere else, publishing is refused, because a tag is never moved. Launchway can create the tag when the Flight reads a repository it hosts, or has a `push:` section (see [Editing pipelines](editor.md)); without one, create the annotated tag yourself (`git tag -a v1.5.0 <commit> && git push origin v1.5.0`) and publish, and Launchway adopts it. Tags are not signed: Launchway has no signing key for Git objects, so there is nothing to claim. Image signing is separate (see [Container images](images.md)).

A published release's tag is checked again when it is promoted: if the tag no longer points to the released commit, nothing is promoted and the release page says so.

### Public downloads

By default every download needs a signed-in person who can read the Flight. An installation can allow **public content**: an administrator of every Flight turns it on (`launchway release installation --public-downloads=true`, off by default), a Flight administrator asks for it in the Flight's settings (`--public`), and a release is made public when it is published (admin only). All three must agree; withdrawing a release stops its public download at once. Public files are at `/public/releases/PROJECT/VERSION/FILE`.

### The latest release and download counts

`latest` stands for the version wherever a version goes: `launchway release show web latest`, `GET /api/v1/projects/web/releases/latest` for the page, and `.../releases/latest/assets/app.apk` for a file by name, so a link in a README never needs a new number. It names the newest published release that is not a pre-release; a withdrawn release, a draft and a candidate are never it. Where public downloads are allowed (the three yeses above) the same words work without signing in, at `/public/releases/PROJECT/latest` for the page and `/public/releases/PROJECT/latest/FILE` for a file; if the latest release was not made public, they answer not found rather than showing an older one.

The list marks the **Latest** release and each **Pre-release**. Each file counts its downloads, shown on the release page, in `launchway release show` and as `downloads` in the API. A download counts when it was sent to the end without a break: a whole file counts once, the rest of a broken download counts once (the part before it does not), and a `HEAD`, a part in the middle and a refused request count nothing.

## Release branches and urgent fixes

To release from a maintenance branch, create the release from that branch and say the version is lower than the highest on purpose:

```
launchway release create --project shop --version 1.4.2 --from release/1.4 --allow-lower --generate-notes
```

The notes are written from the changes since the previous lower release of the same Flight. Backporting a fix to the maintenance branch is a pull request that cherry-picks the commit: open it on the hosted repository as usual (a pull request from a branch made with `git cherry-pick`), merge it, and release from the branch. Launchway does not cherry-pick for you yet; the roadmap says so.

## With a pipeline

The pipeline language is not changed for releases; the steps run the `launchway` command with a token from a secret store. The library template `release-publish` (in the example library) does the drafting and publishing, and two example pipelines in `examples/release/` use it:

- *Tag, build, attach, publish*: pushing a tag `v*` builds, then drafts a release with notes, attaches the Cargo and image digests of that run, and publishes it.
- *Merge, draft a candidate, approve, publish*: a merge to the default branch drafts the next release as a candidate; a person with the operator role gives their Clearance on the approval step; the release is published.

A promotion starts a pipeline too. See [Promoting a release](promoting-releases.md) for the inputs it expects.

## Launchway's own release

Launchway releases itself with the workflow in `.github/workflows/release.yml` (`packaging/release/release.sh`): binaries, packages, charts, SBOMs, `SHA256SUMS`, a signature and provenance, on a pushed `v*` tag. That workflow is unchanged. The same release run as a Launchway release pipeline is in `examples/release/launchway-itself.yaml`: the step runs `packaging/release/release.sh` and keeps its output as Cargo, and the next step drafts the release with notes from the merged pull requests, attaches `SHA256SUMS`, its signature and the packages, and publishes it. Moving over is a matter of running both for one release and comparing the checksums.
