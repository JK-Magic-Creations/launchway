# Home panels: the contract

Home is a grid of panels. A feature that wants a place on Home adds a panel type; it does not edit the Tower.

A **panel type** is an entry of `PANEL_TYPES` in `web/src/pages/panels/registry.ts`:

```ts
{ type: "note", label: "Notes and links", description: "...", defaultSize: 2,
  fields: [{ key: "text", label: "Text", kind: "textarea", required: true }],
  load: () => import("./NotePanel") }
```

- `load` is a lazy chunk: a panel nobody has costs nothing, and the initial bundle does not grow.
- `fields` are what a person fills in when they add one (`text`, `textarea`, `project`, `address`); the values arrive as `settings`.
- The component receives `PanelProps`: `id`, `title`, `settings`, `size` (1, 2 or 3 columns), the one `home` answer Home was drawn from (needs, running, Flights (codebase), favorites) and `now`. It draws its own loading (a skeleton the height of its content), empty and error states; a thrown error must not take Home down, so read data defensively (an older server may not send a field).
- Take data from `home` when it is there, and otherwise ask the server for your own, permission-filtered, small answer. Never put a secret in `settings`: the layout is shared with everyone who can see Home.
- Links in a panel go through the router (`onLink`) and an address from `settings` must be checked to be on this server (`href.startsWith("/")`).

The arrangement is a layout document: page `home`, keys `panels` (the custom ones, each `{ id, type, title, size, settings }`), `order` (every id, built-in and custom), `hidden`, `view` and `open`. The shared default for a level and each person's own layer apply to it through `/api/v1/layouts/home` and `/api/v1/me/layouts/home`, with locks, so a new panel type needs no storage. `cleanPanels` drops what it does not understand, so a document written by a newer version never breaks an older one.

Tests: render `HomeSections` with a fixture (`src/pages/HomeSections.test.tsx`) and check your panel appears, its empty and error states, and that axe finds nothing in light and dark.

A new list that can grow long uses the Explorer instead; see [Every view has an address](views-have-addresses.md).
