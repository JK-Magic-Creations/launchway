# Squadrons and Flights

A **Squadron** is a named folder of Flights. It has its own people, shared settings and a page. A **Flight** is one repository and its pipelines: the level below a Squadron. The word *Project* is no longer used for either; what used to be called a project is now a Flight.

The plain words (when you turn the themed words off) are **Team** for a Squadron and **Codebase** for a Flight. The words are labels only: a Squadron is stored as `unit` and a Flight as `project`, and the API paths, configuration kinds, command-line flags (`--unit`, `--project`), file keys, Terraform resources, tokens and page addresses keep those names. A Flight's page is still `/projects/NAME`; a Squadron's page is `/units/NAME`.

## How today's projects appear

Nothing needs an edit. Every project you had is a Flight with one pipeline, with the same name, the same address and the same history. A project that was filed in a Squadron stays there. The others sit in a built-in view called **Unfiled** (a view, not stored data). A Squadron that existed is still there, with the same settings; the level is now always shown, even if nobody has made one.

## Make a Squadron

Choose **Add a Squadron** (at the top of the pipelines list, and on Tower) and give a name: letters, digits, `.`, `_` and `-`, starting with a letter or a digit. A description is optional, and a display name can come later. Nothing else is asked, and no repository. The new Squadron opens, empty, and offers **Add a Flight**.

```text
launchway squadrons create payments --description "Everything that handles money"
```

Making and changing Squadrons needs the admin role on every Flight today; a person without it is told so on the page. The same is true for the command, the API (`POST /api/v1/units`) and the MCP tool `create_unit`.

## Add a Flight

Inside a Squadron, **Add a Flight** searches the repositories you may see. The hosted repositories that have no Flight yet come first, then the ones that already have one (marked), then, if you choose a connection, the repositories that connection can see. Choosing one makes the Flight in that Squadron and shows what the repository holds: the pipeline file it found, or a starter to commit with the pipeline editor. Connection or credential setup is only offered when there is no hosted repository and no connection to search.

```text
launchway squadrons candidates --query pay
launchway squadrons add-flight payments --repository acme/pay-api
```

The older entry points ("Add a project") lead to the same place. The earlier wizard is still available for a repository that needs a connection or a custom pipeline source.

## Move Flights

Select one or many Flights in a list and choose **Move to Squadron…**. Before anything changes, a preview lists, for each Flight:

- the shared settings (roles, libraries, policies, Airfields, Ramps, secret stores) that stop applying and the ones that start;
- the variables that will resolve to another value, or to none;
- the people and groups who gain or lose the ability to start runs there. Today a Flight has three roles, viewer, operator and admin; operators and admins start runs, and administrators of every Flight are not affected by a move;
- each secret store the pipeline names, and whether it answers from the **same** store, a **different** one, or **none** after the move.

If any secret reference would resolve to a different store, or to none, the move of that Flight is **blocked** with an explanation: a move must never make a pipeline read another store's secrets, or stop working. Give the new Squadron a store with the same definition, or change the reference, and move again. The Flights that are not blocked move in one audited change; one `project.moved` entry is written for each. What was moved, and where each came from, is in the answer, so the move can be undone.

```text
launchway squadrons move pay-api pay-web --to payments --preview
launchway squadrons move pay-api pay-web --to payments
```

## Organize into Squadrons

If many Flights sit in no Squadron, **Organize into Squadrons** proposes some, from the folders you already made with the organize feature and from name prefixes (`payments-api` and `payments-web` suggest `payments`). You can rename a proposed Squadron, merge two, and move a Flight between them or leave it unfiled, then apply: one audited change that makes the Squadrons and files the Flights. It can be undone from the page that follows it.

```text
launchway squadrons organize
launchway squadrons organize --apply --json > organize.json
launchway squadrons organize --undo organize.json
```

## What a Squadron page shows

Its Flights with their latest result and what needs attention; its people and shared settings, with links to the pages that change them; and its **Operations**: the pipelines nothing starts but a person (no push, pull request, schedule or webhook trigger). An empty Squadron says what it is for and offers **Add a Flight**.

## For automation

Everything above is equally available in the HTTP API ([reference](../reference/http-api.md)), the `launchway squadrons` commands ([reference](../reference/cli.md)) and the MCP tools `list_units`, `get_unit`, `create_unit`, `list_project_candidates`, `create_project_in_unit`, `preview_project_move`, `move_projects` and `organize_into_units`. Terraform needs no new resource: `launchway_unit` makes a Squadron and the `unit` of `launchway_project` files a Flight in it.

## What is not here yet

A Flight holds one pipeline today. Several pipelines per Flight, sharing a pipeline with another Squadron, a customizable Squadron page, and custom roles are planned and not built.
