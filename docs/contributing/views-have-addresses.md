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

Already on this rule:

| Page | In the address |
|---|---|
| Every page with `PageTabs` or `Tabs` (`useTab`) | `tab`, a history entry for each choice |
| A Sortie | `tab`, `step`, `tests_step`, `report`, `case`, and the tests tab's `result`, `find`, `tag`, `suite`, `variant`, `order`, `group`; a log line or range as `#L120` or `#L120-L140` (choose a line number; Shift extends) |
| A pipeline | `view` (`text`, `history`), `ref` |
| A pull request | `tab`, `mode`, `since` |
| Dependencies | `eco`, `status`, `q`, `direct`, `vulnerable` |
| Applications | `show`, `q` |
| Templates | `tab`, `version` |
| The explorers (pipelines, repositories) | `q`, `node`, `view`, `result`, `label`, `kind`, `sort`, `group`, `fav`, `mine` |
| The Sorties list | `project`, `status`, `lane`, `show` |

A page names its view for bookmarks and history with `useViewTitle("Tests · run 3f2a1b")`; the shell puts it before the page's title.
Two tests keep the rule: `src/urlState.rules.test.ts` fails when a page shows tabs without `useTab` or `useUrlState`, or writes the address by hand, and `src/urlState.routes.test.tsx` walks pages (address to view, view to address, remount).
Not yet converted: the audit filters, some settings sections, and scroll restoration on Back. The tests in `web/src/urlState.test.tsx` and `web/src/explorer/Explorer.test.tsx` show how to test a page: change a view, read the address, mount again from it, and expect the same view.

## Adding a page to the organizing system

A list page that can grow long uses `Explorer` (`web/src/explorer`): build `XItem`s (a place as a path of levels and folders, a tone, a last result, history), pass them with `useOrganized(area)` for folders, labels, favorites and moves. Load the explorer lazily and keep the old list as the `Suspense` fallback so the page does not jump. A new Home section is a component in `HomeSections.tsx` with an id in `DEFAULT_ORDER` and a title; the arrangement of sections is stored through the layout service (`/api/v1/layouts/home`) so a default and a personal layer apply to it with no more code.
