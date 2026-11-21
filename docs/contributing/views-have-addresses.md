# Every view has an address

A page's view is part of its address. A tab, a filter, search text, a sort, a grouping, a cards or table choice, a selected item, a page of results: if it changes what the page shows, it is in the query string, so a link or a bookmark opens the same view and Back moves between views.

Use `useUrlState` from `web/src/urlState.ts`:

```tsx
const [tab, setTab] = useUrlState("tab", "overview", oneOf("overview", "tests", "history"));
const [q, setQ] = useUrlState("q", "");
```

- A value equal to its default is left out, so links stay short.
- A value that does not parse (a hand-edited address) falls back to the default and never throws. Give a closed set of values a codec (`oneOf`, `int`, `bool`).
- Typing uses the default `"replace"`; choosing a tab or an item uses `"push"` so Back returns to the one before.
- Names are plain words: `?tab=tests&status=failed&step=unit-tests`.
- Personal layout choices (which sections are hidden, a remembered cards or table choice) are saved preferences, not part of the address.

Already on this rule: the explorers (pipelines and repositories), and the Sorties list (`project`, `status`, `lane`, `show`). The tests in `web/src/urlState.test.tsx` and `web/src/explorer/Explorer.test.tsx` show how to test a page: change a view, read the address, mount again from it, and expect the same view.

## Adding a page to the organizing system

A list page that can grow long uses `Explorer` (`web/src/explorer`): build `XItem`s (a place as a path of levels and folders, a tone, a last result, history), pass them with `useOrganized(area)` for folders, labels, favorites and moves. Load the explorer lazily and keep the old list as the `Suspense` fallback so the page does not jump. A new Home section is a component in `HomeSections.tsx` with an id in `DEFAULT_ORDER` and a title; the arrangement of sections is stored through the layout service (`/api/v1/layouts/home`) so a default and a personal layer apply to it with no more code.
