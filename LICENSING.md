# Licensing

This page explains in plain language how Launchway is licensed. It is a summary, not legal advice,
and it is not the licence: the [LICENSE](LICENSE) file is. If your situation is not clear, ask a
lawyer.

## Launchway is free and open source

Launchway is released under the [GNU Affero General Public License, version 3](LICENSE)
(AGPL-3.0). The copyright is held by JK Magic Creations; see [COPYRIGHT](COPYRIGHT).

**Every feature is in the open-source release. Nothing is held back for paying customers.** That
is a standing decision of the owner, not a temporary offer.

## What you can do without asking anyone

- Run Launchway on your own servers or Kubernetes clusters, for any purpose, including at a
  company.
- Read the code and change it to suit your needs.
- Use your modified version inside your own organisation. You have no obligation to anyone to
  publish your changes if you only use them yourselves.
- Copy and share Launchway, under the same licence.

## What the AGPL asks of you

The AGPL is a "copyleft" licence, and it adds a rule for software that people use over a network.
If you **modify Launchway and let other people use your modified version over a network**, you
must offer those users the complete source code of your modified version, under the AGPL. If you
**distribute** Launchway or a modified version, you must pass on the same rights you received.
Section 13 of the [LICENSE](LICENSE) has the exact wording; read it, not this summary, to decide
what applies to you.

## If your organisation cannot accept that

Some organisations cannot, or prefer not to, accept the AGPL's terms, for example because they
want to offer a modified Launchway to others without publishing their changes. For them, JK Magic
Creations can offer a **commercial licence**, which replaces the AGPL's terms with agreed terms.
It gives no features that the open-source release lacks.

To ask about a commercial licence, write to
[jkhunter@jk-magic-creations.com](mailto:jkhunter@jk-magic-creations.com). Terms are agreed case by
case; nothing on this page is an offer or a promise of any price or term.

The security scans (govulncheck, osv-scanner, gitleaks, Trivy, kubeconform, kube-linter, hadolint,
OWASP ZAP, OpenSSF Scorecard and Syft) run only in development and in the build pipeline, from
containers or `go run` at pinned versions. They are not linked into, bundled with or shipped in
Launchway. Their licenses are listed in
[docs/security/how-launchway-is-scanned.md](docs/security/how-launchway-is-scanned.md); hadolint is
GPL-3.0 and is only ever run as a separate program.

## Contributing

JK Magic Creations can offer a commercial licence because contributors license their contributions
to it under the [contributor licence agreement](CLA.md). Contributors keep their copyright. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## Fonts in the web interface

The web interface bundles three fonts, so it looks the same everywhere and never fetches a font
from a network: Inter, Montserrat and JetBrains Mono. All three are licensed under the SIL Open
Font License 1.1, which allows bundling and redistribution with software. Their copyright notices
and the licence text are in [web/src/assets/fonts/LICENSES.txt](web/src/assets/fonts/LICENSES.txt).

## Tools used only to build and test

The accessibility checks use two npm packages that are development dependencies of the web
interface. They are never part of the interface's bundle, the binary or a release:
axe-core (Mozilla Public License 2.0, the scanner that finds accessibility problems) and
playwright-core (Apache License 2.0, which drives a browser for the browser scans). The MPL-2.0 is
a file-level licence: it asks that changes to axe-core's own files be shared, and does not reach
code that only runs it. `web/package.json` pins both versions exactly.
