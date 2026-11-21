# Releases

A **release** is the thing you version, clear, promote and report on. It is not a tag with a zip: it is a
version, the commit and annotated tag it was made from, notes, Cargo (files with their checksums), the image
digests its Sortie built, and a record of who did each step.

## One release, many Airfields

The thing that moves from Airfield to Airfield is the release. Nothing is built again on the way: a promotion
starts the project's pipeline at the release's tag with the release's version and image digest, so the thing that
passed in `dev` is exactly the thing that arrives in `prod`.

## Fixed once published

A release goes from **draft** to **candidate** to **published**. A published release never changes: its version,
commit, tag, Cargo and digests are fixed, and even an administrator cannot edit them. If something is wrong you
withdraw the release, with a reason, and make a new one.

## What stands in the way

Each Airfield may ask for a **checklist**: notes written, checks green on the commit, no blocking findings, a soak
time in the previous Airfield, Clearance from named roles, a change window, no freeze. Every line shows whether it
is met and why not. An administrator may override a window, a soak time or a freeze, with a reason that is kept
in the Logbook; nothing else can be overridden.

## After the release

The [release report](app:/deployments/releases/report) works out how often you launch, how long a change takes to
reach each Airfield, how many launches fail or are returned to base, and how long it takes to restore service,
from the deployments Launchway already records.

Read more: [Making a release](../guide/releases.md), [Promoting a release](../guide/promoting-releases.md),
[Release checklists, windows and freezes](../guide/release-checklists.md), [Measuring delivery](../guide/measuring-delivery.md).
