# Organizing and finding things

A few pipelines fit on one page. A few hundred do not. Launchway lists pipelines, repositories and Sorties in an **explorer**: a tree of places on the left, the chosen place's items on the right, and a filter box that narrows both as you type.

## The places

The tree follows the levels, then the folders inside them: Base, then your Wing, then each Squadron, then folders. Each place shows how many items are in it and, when something there failed, how many, as a mark with a word and not only a color. A folder is shown only when you can see something in it, so a count never tells you about work you may not see.

A pipeline or repository can sit in one folder. There are two ways to make folders:

- **By name.** A name with slashes, such as `payments/api/build`, shows as the folders `payments` and `api` with no one filing anything. This is the starting point and needs no setup.
- **By hand.** Someone who may arrange for everyone at that level makes a folder, names it, and files items in it. Choose **Arrange for everyone**, then drag an item onto a folder, or use **Move to…** on the item, which works with the keyboard and a screen reader. The last move can be undone from the line that appears, and every move is written to the Logbook.

**Labels** are free tags for groups that cut across folders, such as a team, a tier or a language. Anyone who may operate a Flight (codebase) can label it; the label filter and the group-by choice use them.

## My collections

**Arrange for me** (on every explorer) makes your own groups, called collections. An item you add shows in the collection and still lives where it was for everyone else: nothing moves for anyone. Drag an item onto a collection, or use **Add to collection…**. Collections follow you across devices.

## Cards or table, search and filters

Show the items as **Cards** (name, where it lives, last result with its age, a strip of recent results with a text alternative, what runs now, and the actions that exist) or as a compact **Table**. Search matches the name, description, labels and the place, forgives a typo, and marks what matched. Press **f** to jump to the filter box (**/** is always the finder; **?** lists every shortcut). You can filter by status, label, kind, favorites and "mine", sort, group by folder, label or status, and expand or collapse the whole tree.

Everything you choose is in the address, for example `/pipelines?q=ledger&result=bad&view=table`, so a link or a bookmark opens the same view and Back goes to the one before. **Copy link to this view** copies it.

Star an item to make it a favorite. Favorites are yours: they show first on Home and in the finder, and a favorite pipeline's failures always matter to you.

## Finding anything

Press **/** (or choose the search field in the top bar) to open the finder. Before you type it offers your favorites and what you opened lately. As you type it searches the pages and actions, the documentation (from the copy in the program, so it works offline) and, on the server, the Flights, repositories, folders, open pull requests and Sorties you may see, and, if you may manage access, the people: a Sortie is found by its number (`#412`), the start of its commit, or its branch, a pull request by its number or title. Results are ranked (exact name, then start of a name, then a word in it, then anywhere in it, then a near miss) and grouped by kind. The server only searches what you are allowed to read.

## Layouts: a default for everyone, and your own

Each page has a **shared default** set by someone who may arrange at a level: Base, Wing, Squadron or Flight. A lower level inherits the one above it and may change it, unless a level above **locks** the layout. On top of that, **Arrange for me** changes only your own view: the order and visibility of the sections on Home, which Squadrons are open, cards or table. Your arrangement is kept on the server, so it follows you across devices, and a note on the page says when you are looking at your own. **Reset to the default** puts the shared one back. An item added to the shared layout appears for you in its default place; one removed disappears.

A level's layouts and folders can be exported as one document and applied again, so the defaults can live in Git:

```sh
launchway organize export --scope base > organization.json
launchway organize import organization.json --dry-run
launchway organize import organization.json
```

An import adds and updates what the document names and never deletes anything it leaves out.

## Pinning a view to Home

**Pin to Home** on any explorer makes a panel on your own Home that links back to the view exactly as you filtered it. See [the Tower](home-page.md) for panels.

## From a script or an agent

`launchway organize list|folder|move|label|export|import` and the MCP tool `organize` do what the page does, with the same permissions. See the [reference](../reference/organizing.md).
