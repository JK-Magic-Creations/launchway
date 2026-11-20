# Writing the docs

How the documentation is organised, built and checked, for anyone changing it.

## Where things are

| Path | What |
|---|---|
| `docs/*.md`, `docs/<section>/*.md` | The pages, in Markdown. They read well on GitHub and build the site. |
| `docs/.vitepress/config.mts` | Site settings: navigation, sidebar, search, base path, the hosted demo link (`demoUrl`). |
| `docs/.vitepress/theme/` | The landing page (`Landing.vue`) and the Mermaid renderer (`Mermaid.vue`). |
| `docs/public/` | Files served as they are: `roadmap.html` (the interactive roadmap, generated), the logo, landing images. |
| `docs/roadmap/` | The roadmap's data and page template. |
| `docs/images/` | Screenshots used by pages, referenced with relative paths. |
| `scripts/docs/` | Checks run by `make docs`, and the screenshot scripts. |

Sections follow the reader: *Start here* explains, *Tutorials* teach, the *User guide* describes
each feature, *How-to guides* solve tasks, *Reference* lists everything exactly, and *Security*
covers trust, hardening and compliance.

## Build and preview

You need Node 24 or newer.

```sh
make docs          # install the pinned tools, build the site, check links, anchors, spelling, roadmap
make docs-serve    # a live preview at http://127.0.0.1:5173/launchway/
```

The built site is in `docs/.vitepress/dist`. After `npm ci` has run once, the build needs no
network access.

## Checks

- **Dead links**: the site build fails on a link to a page that does not exist.
- **Anchors**: `scripts/docs/check-links.mjs` checks every link and `#anchor` in the built site.
- **Spelling**: `cspell` with `docs/cspell.json`; add real words to its list.
- **Roadmap**: `scripts/docs/build-roadmap.mjs --check` checks that `public/roadmap.html` and
  `roadmap-list.md` were generated from the current `roadmap/roadmap.json`.
- **Diagrams**: `make docs-diagrams` renders every Mermaid diagram of the built site in a headless
  browser, in light and dark, and fails on any diagram error. It needs a Chromium: run
  `cd docs && npx playwright-core install chromium` once, or set `LAUNCHWAY_DOCS_CHROME` to one.
- **References**: `make docs-reference` runs Go tests that compare the pipeline, configuration,
  Helm values, HTTP API, events, roles and policy rule references with the code, and checks every
  pipeline example in the pipeline reference with `launchway validate`. The same tests run in
  `make test`.

Pull requests run all of these.

## Updating the roadmap

The roadmap's data is `docs/roadmap/roadmap.json`: the items available today, and the items in
progress and planned, each with a rank (by value), a theme and a one-line value statement.
It holds only those fields: notes on progress and work that is not about the product stay out.
Edit the data, or run `make docs-roadmap-sync ROADMAP_JSON=path/to/roadmap.json` to write it from
a maintainer's roadmap file (the script keeps the public fields, drops the items that are not
about the product, renumbers the ranks and refuses text on its deny list). Then run
`make docs-roadmap` to regenerate `docs/public/roadmap.html` (the page, from
`docs/roadmap/template.html`) and `docs/roadmap-list.md`. `make docs` fails if they are stale.

## Screenshots

`scripts/docs-screenshots.sh` builds Launchway, starts a server with demo data, and captures the
screenshots in `docs/images/tour/`. Run it after UI changes.

## Publishing

The workflow `.github/workflows/docs-pages.yml` builds the site and deploys it to GitHub Pages on
every push to `main` that changes the docs. It needs the repository's Pages source set to
*GitHub Actions*. Pull requests run `.github/workflows/docs.yml`, which builds and checks without
publishing.

See also the [style guide](style.md).
