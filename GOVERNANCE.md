# Governance

Launchway is maintained by one person today. This file says how decisions are made now, and how
that is meant to change, so that contributors and users know what to expect.

## Who decides

- **The maintainer** owns the project's direction, merges every change, publishes releases and
  handles security reports. The maintainer acts for JK Magic Creations, which owns the code (see
  [LICENSING.md](LICENSING.md)).
- **Contributors** propose changes by pull request and ideas by issue or discussion. Anyone can
  take part, under the [code of conduct](CODE_OF_CONDUCT.md) and, for code, the
  [CLA](CLA.md).

## How decisions are made

- **Small changes** (bug fixes, documentation, tests) are decided in the pull request: the
  maintainer reviews, asks for changes or merges. A change is done when the gates in
  [CONTRIBUTING.md](CONTRIBUTING.md) pass and someone other than its author has read the diff.
- **Larger changes** (a new feature, a new setting, a change to the database schema, a change to
  what a security control does) start as a discussion or an issue, so the reasoning is written down
  before the work. The maintainer decides, and says why in the thread, including when the answer is
  no or not now.
- **Security problems** are handled privately first, as [SECURITY.md](SECURITY.md) describes.
- **The roadmap** is public in the documentation. The maintainer sets its order; a contributor who
  wants something earlier can say what it is worth and offer to build it.
- **Disagreements** are settled by the maintainer after discussion in the open. Conduct matters
  are handled as the code of conduct says.

## Releases

The maintainer tags a version, the release workflow builds and signs it as a draft, and the
maintainer checks the draft and publishes it ([SUPPORT.md](SUPPORT.md) says what is supported and
how to verify a download).

## Becoming a maintainer

Because there is one maintainer, there is a single point of failure, and the aim is to remove it.
A contributor who has shown, over several accepted changes, good judgment in review and care for
security and compatibility can be invited to become a maintainer: they are given review and merge
rights, and the repository's protections (required reviews and checks) then apply to them as to
everyone else. The invitation is the maintainer's to make and is recorded in this file.

## How this changes

When there are two or more maintainers, this file is updated to say:

- that decisions on larger changes need agreement of two maintainers or, if they disagree, are
  settled by a written proposal and a vote of the maintainers;
- that no maintainer merges their own change without another maintainer's review;
- how a maintainer steps down or is removed (inactivity of a year, or a breach of the code of
  conduct), and who holds the release and signing rights meanwhile.

If the maintainer can no longer continue and no successor is named, the project is archived with
a notice here rather than left to look maintained; the license lets anyone fork it.

Changes to this file are made by pull request like any other, and take effect when merged.
