# Cut a release

Cutting a release is one button: it asks for the version, shows what will be tagged and what the notes will say, and then makes the annotated tag, attaches the files the pipeline built and publishes the release. This page is the short path. [Making a release](releases.md) has the whole life of a release, and [Promoting a release](promoting-releases.md) what comes after.

## Before you start

- The Flight (codebase) reads a repository Launchway hosts (Git hosting is on, `forge.hosting`), or has a `push:` section so Launchway may write tags. A hosted repository needs nothing else.
- The pipeline runs on the default branch and keeps the files you want to ship as Cargo (see [Cargo](artifacts.md)). For an Android app that is the package:

```yaml
steps:
  package:
    image: registry.example.internal/tools/android-build:latest
    run: ./gradlew assembleRelease && mkdir -p dist && cp app/build/outputs/apk/release/app-release.apk dist/hub-app.apk
    artifacts: [{ name: dist, paths: ["dist/*"] }]
```

Every file the Sortie kept at the release's commit is copied into the release, with its SHA-256, and the Sortie's image digests are recorded. Nothing else is needed in the pipeline.

## Cut it

1. Open the Flight's page (Code, then the Flight) or the repository's page and choose **Cut a release**. Only people who may operate the Flight see it.
2. The version is already filled in with the next one, read from the merged pull requests and commits since the last release (a `feat:` or a `feature` label asks for a minor version, a breaking change for a major one). Change it if you like. It is checked as you type, with a plain message: for the default scheme write three numbers, `1.4.0`, and leave the `v` off.
3. Read the box that says what will be tagged: the tag (the version with the Flight's tag prefix, `v` by default, so `1.4.0` is the tag `v1.4.0`), the commit and branch, and where it is kept.
4. Read the notes. They are written from the pull requests and commits since the previous release; the commits a merged pull request brought are part of that pull request and are not listed twice.
5. Leave **Attach the files built at this commit** ticked to carry the files of the latest successful Sortie at that commit. If none succeeded there, the dialog says so; run the pipeline first, or add files from the release's page.
6. Choose **Cut 1.4.0**. The tag is made in the hosted repository (annotated, by you), the release is published and its page opens.

If publishing fails after the draft was made (the tag exists somewhere else, say), the draft stands and the message says what to fix; publish it from its page.

The tag is an ordinary ref of the hosted repository: a push mirror copies it to the outside host with the other refs, under the repository's mirror rules.

The same from the command line, the API and an agent:

```sh
launchway release next --project hub
launchway release create --project hub --version 1.4.0 --generate-notes --attach-cargo --publish
```

`POST /api/v1/projects/hub/releases` with `{"version": "1.4.0", "generate_notes": true, "attach_cargo": true, "publish": true}`; the MCP tool is `create_release` with the same fields.

## Install it on a phone

The release's page has a **Get it on a phone** sheet with a QR code. For a release with an Android package the code downloads the package: point the phone's camera at it, open the link, sign in if asked (you are returned to the download), and open the file when it has downloaded. Android asks once to allow installing apps from the browser. Each file in the list below also has **Download** and **Open on a phone**, which shows a code for that file alone. The code is drawn in your browser: nothing is sent anywhere to make it.

The address in the code is this server's address as your browser knows it. A phone must be able to reach that address (on the home network, or over the VPN).

By default a download needs a signed-in person who can read the Flight. A release is downloadable without signing in only when the installation allows public content, the Flight asks for it and the release was published public ([Making a release](releases.md#public-downloads)); then the code holds the public address.

Downloads are streamed, never held in memory, and answer `Range`, so a phone resumes a broken download. Each one is recorded in the Logbook as `release.asset.download`.

## Publishing to an outside Git host

Launchway does not yet create a release on the outside host a push mirror copies to; the tag arrives with the mirror and the release is made there by hand. The design for doing it from Launchway is in [Publishing a release to an outside Git host](../development/release-publish-outside.md).
