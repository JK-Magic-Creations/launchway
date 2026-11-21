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

## The documentation inside the product

The same sources are shipped in the Launchway binary and shown in its **Learn** area, so every
installation has the documentation of exactly the release it runs, offline.
`scripts/docs/build-learn.mjs` renders the pages (with the site's own Markdown renderer) into
`web/dist/learn-content` as gzipped JSON plus a prebuilt search index; Go's `embed` takes them along
with the web UI. `make build` and the image build run it; it needs no browser and no network.

- Which pages are in Learn, and in what order, is `docs/learn/nav.json`. A page that is in no
  section, or a section naming a page that does not exist, fails the build. The roadmap, the
  contributing pages and the demo stay on the public site only.
- Link to a page of the product with `[Waiting for a decision](app:/activity/approvals)`. In Learn it
  is a link; on this site it is the plain text. A test (`web/src/learn/docs.test.ts`) fails when the
  route is not a page of the product, and when any link from the product into Learn names a page or a
  heading that does not exist.
- A `<!-- try -->` line before a ```` ```yaml ```` block gives the block a "Try it in the editor"
  button in the product, which opens the pipeline in the playground.
- Mermaid diagrams are drawn at build time as SVG, light and dark, and kept in
  `docs/.vitepress/diagrams`. After adding or changing a diagram run `make docs-diagrams-cache`
  (needs a Chromium: `LAUNCHWAY_DOCS_CHROME=/path/to/chrome`) and commit the new files;
  `make learn-check` (part of `make docs` and `make check`) fails when one is missing or unused.

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

The roadmap's data is `docs/roadmap/roadmap.json`: the three-sentence vision, the things available
today, and the milestones (marked Now, Next or Later) with their groups and items. Each item has a
status (available, in progress or planned), a title, one plain sentence of what it is and why it
matters, and, when a page describes it, a link to that page. There are no dates and no internal
identifiers: the script refuses text on its deny list, a link to a page that does not exist, and
fields it does not know. An item moves to available only when it is on the main branch, tested and
documented. Edit the data, then run `make docs-roadmap` to regenerate `docs/public/roadmap.html`
(the page, from `docs/roadmap/template.html`) and `docs/roadmap-list.md`. `make docs` fails if they
are stale. The wording follows the originality rule: no other product is named for comparison.

## Screenshots

`scripts/docs-screenshots.sh` builds Launchway, starts a server with demo data, and captures the
screenshots in `docs/images/tour/`. Run it after UI changes.

## Publishing

The workflow `.github/workflows/docs-pages.yml` builds the site and deploys it to GitHub Pages on
every push to `main` that changes the docs. It needs the repository's Pages source set to
*GitHub Actions*. Pull requests run `.github/workflows/docs.yml`, which builds and checks without
publishing.

See also the [style guide](style.md).
