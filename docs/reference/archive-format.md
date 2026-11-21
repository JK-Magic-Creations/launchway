# Archive format

A repository can hold a read-only record of another server's issues and their comments, so that history written before a move stays readable and searchable in Launchway. It is an archive, not an issue tracker: nothing in it is created, edited, closed or assigned here. It appears as the **Imported history** tab of the repository, and through the API, the CLI and the MCP server.

## Getting issues in

1. Export from the old server with your own token. The script `scripts/export-issues.sh` reads the source server's REST API (issues at `/api/v1/repos/OWNER/NAME/issues`, their comments below each issue) and writes the document below to standard output. It needs `curl` and `jq`, reads the token from the environment variable `SOURCE_TOKEN` (never from an argument), and changes nothing on the source.

   ```text
   SOURCE_TOKEN=... scripts/export-issues.sh https://git.example.org OWNER/NAME > issues.json
   ```

2. Store it. The repository's administrators run:

   ```text
   launchway repos import-archive OWNER/NAME issues.json
   ```

   The command sends 200 issues at a time. Importing the same file again replaces each issue (and its comments) by number and changes nothing else, so a fix to the export is imported the same way. A failure part-way says how many issues are stored; run the command again to continue.

3. Read it: the **Imported history** tab, `launchway repos archive list|show`, the MCP tools `list_archived_issues` and `get_archived_issue`, or the [HTTP API](http-api.md).

## The document

```json
{
  "format": "launchway-archive/1",
  "source": { "name": "git.example.org", "url": "https://git.example.org/acme/shop" },
  "exported_at": "2026-11-21T10:00:00Z",
  "issues": [
    {
      "number": 12,
      "title": "Checkout crashes on empty carts",
      "state": "closed",
      "author": "bob",
      "body": "Steps to reproduce...",
      "labels": ["bug"],
      "milestone": "v1.4",
      "assignees": ["ada"],
      "url": "https://git.example.org/acme/shop/issues/12",
      "is_pull_request": false,
      "created_at": "2024-01-02T03:04:05Z",
      "updated_at": "2024-01-03T03:04:05Z",
      "closed_at": "2024-01-03T03:04:05Z",
      "comments": [
        { "id": "77", "author": "ada", "body": "Fixed in the next release.", "created_at": "2024-01-03T01:00:00Z" }
      ]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `format` | `launchway-archive/1`; absent is accepted, another value is refused |
| `source` | where the issues came from, shown on the tab (`name`, `url`) |
| `issues[].number` | the number at the source, a positive whole number, unique in the document; it is the key: importing it again replaces it |
| `issues[].state` | `open` or `closed` |
| `issues[].labels`, `assignees` | lists of names, at most 100 each |
| `issues[].is_pull_request` | `true` for the source's record of a pull request kept in its issue list (the script writes these only with `--with-pulls`) |
| `*.created_at`, `updated_at`, `closed_at` | RFC 3339 times; may be empty |
| `comments[]` | `id` (the source's, as text), `author`, `body`, `created_at`, `updated_at`; kept in the order given |

Limits: 500 issues a request, 5,000 comments an issue, 1 MiB for a body or comment, 1,000 bytes for a title. A document that breaks one is refused whole and stores nothing. Text is stored as text and shown as plain text, never as HTML.

## What stays out

The archive holds issues and their comments only. Labels and milestones are kept as names on each issue, not as objects; reactions, attachments, time tracking and the source's pull request reviews are not exported. Pull request branches are in Git: the source's pull request refs are kept under `refs/imported/pull/` (see `launchway repos verify`).
