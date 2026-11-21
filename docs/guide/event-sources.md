# Event sources

An event source is an address other systems call to start a Sortie (run). Use one when a system that is not a Git host
(a release tool, a ticket system, a monitoring alert, another team's service) should start a pipeline, with values
taken from what it sends. Unlike a [webhook trigger](triggers.md#webhooks), which a pipeline file declares and the
server's configuration secures, an event source is made in Launchway, keeps its own sealed signing secret, can check
the body against a schema, and can filter, deduplicate and hold calls back. Every call is an [event](events.md).

## Make one

```sh
launchway event-sources create payments --name deploy-request \
  --map version=body.release.version \
  --map environment='default(body.env, "dev")' \
  --filter 'body.action == "released"' \
  --dedupe-key body.release.id \
  --schema-file release.schema.json
```

The answer shows the address (`/api/v1/projects/payments/sources/deploy-request`) and the **signing secret, once**.
The secret is sealed under the installation's root key, so a server with no root key refuses to make a source (409,
with how to make a key). Replace a secret with `launchway event-sources change payments deploy-request --rotate-secret`;
the old one stops working at once.

## Sign each call

Send the JSON body to the address with two headers:

```text
X-Launchway-Timestamp: 1763721600
X-Launchway-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>" with the secret>
```

A call is accepted if its timestamp is within five minutes, and **once**: the same signature again is refused with
409 and recorded. A call that cannot prove itself, and a call to a source that does not exist or is off, get the same
`404`, so the address reveals nothing. A failed call counts towards the limit on failed attempts from its address.
The refused call is still an event, shown as not verified, so an attempt to guess a secret is visible.

## What happens to a call

1. **Limits.** Over the source's calls a minute (60 by default; per server), or larger than its body limit (64 KiB by
   default, at most 1 MiB): refused, recorded.
2. **Schema.** If the source has a JSON Schema, the body must match it; the problems are listed (refused, 422). A
   schema may not refer to another address: put what it needs inside it.
3. **Filter.** An expression that must be true. `body.action == "released"`: anything else is recorded as
   *filtered*, with the filter in the reason, and starts nothing.
4. **Mapping.** One expression for each input. The values are checked exactly like a person's, against the
   pipeline's inputs at the source's `ref` (the default branch if none); a value that does not fit is an error that
   says which input.
5. **Dedupe key.** An expression; a second call with the same key within a week is *duplicate*, and its reason names
   the first event. A call that could not start a run does not use up its key.
6. **Window.** With `--debounce N` the Sortie starts once N seconds pass with no new call, using the newest; with
   `--batch N` it starts N seconds after the first call, using the newest. The calls folded in say so.
7. **Start.** A run with event `webhook`, the source's name as its webhook and `source:<name>` as its actor, subject
   to the pipeline's lane and the installation's limits like any Sortie.

Every step ends in a decision with its reason on the [Events](events.md) page.

## Expressions

A small language over `body` (the JSON), `headers` (the call's headers, without any that carry a credential) and
`source` (its name). It cannot call out, loop or read anything else, and its size is limited.

| Form | Example |
|---|---|
| a path | `body.release.version`, `body.items[0].name` |
| literals | `"text"`, `12`, `true`, `null`, `["a", "b"]` |
| comparison | `==`, `!=`, `<`, `<=`, `>`, `>=` (numbers compare as numbers) |
| tests | `body.env in ["prod", "stage"]`, `body.labels contains "ship"`, `body.ref startsWith "refs/tags/"`, `body.tag glob "v1.*"` |
| logic | `&&`, `||`, `!`, parentheses |
| functions | `lower`, `upper`, `trim`, `len`, `default(x, y)`, `join(list, ",")`, `string` |

A path that is missing is `null`, not an error, so `body.action == "released"` is simply false for a body without
`action`. A filter must give true or false; anything else is an error that the event says. An expression that cannot
be read is refused when the source is saved.

## Try it first

```sh
launchway event-sources test payments deploy-request --body sample.json
```

shows whether the body would be accepted, the inputs the run would get, and the dedupe key, and records and starts
nothing. The page for the project in the web interface has the same button.

## Safety

- The secret is never shown again, never in a list, a log, an event or the Logbook (`event_source.*`).
- The body is kept in the event with credential-looking values removed, cut at 16 KiB. Do not send secrets on
  purpose.
- A source can start only what a person who may operate the project could start, and only the pipeline at its `ref`.
- Delete a source and calls to its address are answered not found at once; its events stay.
