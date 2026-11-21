# Toggle protocol

The HTTP protocol an application (or an SDK in a language that has none yet) speaks to read toggles. It has four
calls, an evaluation algorithm, and a file of test vectors that every implementation must pass. Everything here is
version 1 of the ruleset format (`"schema": 1`); a client that sees a higher schema should refuse it and say to upgrade.

## Keys

Every call carries an SDK key, made in the web page or with `launchway toggles keys create`, for one Airfield (environment) of
one project: `Authorization: Bearer lwt_srv_...` (a **server key**, a secret) or `lwt_cli_...` (a **client key**,
public; `X-Launchway-Client-Id` carries it as well). A missing, unknown, revoked or expired key is `401` and says nothing
about which. A call a key's kind may not make is `403`. `429` carries `Retry-After`.

## Calls

| Call | Key | Answer |
|---|---|---|
| `GET /api/sdk/v1/ruleset` | server | The Airfield's ruleset (below) as JSON, with an `ETag`. `If-None-Match` with a current tag is `304`. |
| `GET /api/sdk/v1/stream` | server | `text/event-stream`. Event `ruleset` with `id` the ETag (no quotes) and `data` the ruleset JSON, sent on connection and whenever the ruleset changes. `: ping` comments keep it open. `retry: 3000` is sent first. A key may have 20 streams open. |
| `POST /api/sdk/v1/evaluate` | client or server | Body `{"context": {...}, "toggles": ["a", "b"]}` (toggles optional: all). Answer `{"environment": "...", "values": {"a": {"value": ..., "reason": "..."}}}`. A client key gets only client-visible toggles; others, and unknown ones, are `reason: "not_found"`. A client key's answer has no rule ids. At most 100 attributes, 200 toggles, 64 KiB. Allowed origins are enforced when the client key names some (`403` otherwise). `OPTIONS` answers a browser's preflight. |
| `POST /api/sdk/v1/telemetry` | server | Body `{"counts": [{"toggle", "value", "reason", "count"}]}`, at most 500: aggregated evaluations, no context. Answer `{"accepted": n}`. Once a minute is plenty (the limit is 12 per minute). |

### Resuming a stream

Every event is the whole ruleset, so there is no history to replay. A client that reconnects sends
`Last-Event-ID` with the id of the last event it applied. If that is the current ruleset the server sends nothing
until it changes; otherwise it sends the current ruleset first. A client that was away misses nothing it needs.
While the stream is down, poll `GET /api/sdk/v1/ruleset` with `If-None-Match`.

## The ruleset

```json
{
  "schema": 1,
  "environment": "production",
  "toggles": {
    "checkout-v2": {
      "key": "checkout-v2", "type": "boolean", "kind": "release", "client_visible": false, "salt": "checkout-v2.ab12cd34",
      "on": true, "off_value": false,
      "prerequisites": [{"toggle": "payments-enabled", "value": true}],
      "rules": [{"id": "staff", "clauses": [{"attribute": "email", "op": "suffix", "values": ["@example.com"]}], "serve": {"value": true}}],
      "fallthrough": {"rollout": {"by": "key", "buckets": [{"value": true, "weight": 500}, {"value": false, "weight": 9500}]}}
    }
  },
  "segments": {"beta": {"included": ["u1"], "excluded": [], "rules": [{"clauses": [{"attribute": "plan", "op": "equals", "values": ["beta"]}]}]}}
}
```

A clause is `{attribute, op, values, negate}`. Operators: `equals` (the first value), `in`, `prefix`, `suffix`,
`contains`, `exists`, `in_segment` (any named segment; no attribute), `semver_eq|lt|lte|gt|gte`,
`number_eq|lt|lte|gt|gte` (first value). A weight is in hundredths of a percent; a rollout's weights add up to 10000.

## Evaluation

For a toggle `key` and a context (a JSON object; `key` is the bucketing key):

1. No such toggle: `{value: null, reason: "not_found"}`; the caller's default applies.
2. `on` is false: `off_value`, reason `off`.
3. Each prerequisite is evaluated for the same context (at most 10 deep); unless its value equals the required one
   (same type, same value) the answer is `off_value`, reason `prerequisite_failed`.
4. Rules in order. A rule matches when **every** clause matches. The first match serves, reason `rule_match` with its `rule_id`.
5. Otherwise the fallthrough serves, reason `fallthrough`.

Serving: a `value`, or a `rollout`. A rollout takes the context attribute `by` (default `key`) as text, hashes it
(below) and picks the share whose range holds the bucket. If the context lacks the attribute (or it is empty or not
a string, number or boolean) the answer is `off_value`, reason `missing_bucket_key`.

A clause: no attribute in the context (or null) is **false**, and `negate` turns it into true. A list attribute
matches when any element does. `equals` and `in` compare same-typed values only (the text `"1"` is not the number
`1`). `prefix`, `suffix`, `contains` need text. Number operators need JSON numbers. Version operators need text that
is a semantic version (`1.2.3`, optional leading `v`, pre-release and build parts allowed; build metadata is ignored;
numbers without leading zeros); anything else is false. A segment contains a key unless it is excluded; it contains
it if it is included or if any of its rules matches (all clauses of a rule). A segment does not name segments.

### Bucketing

```
bucket = uint32_big_endian(SHA-256(UTF-8(salt + "." + value))[0:4]) mod 10000
```

`value` is the attribute as text: a string as it is; a whole number as its digits (`42`); another number as the
shortest text that reads back the same (`1.5`); a boolean as `true` or `false`. Shares are laid end to end in the
order written: a bucket below the first weight gets the first value, and so on.

## Test vectors

[`sdk/vectors/evaluation.json`](https://github.com/JK-Magic-Creations/launchway/blob/main/sdk/vectors/evaluation.json)
holds `hashes` (salt, value, bucket) and about ninety `cases`: a ruleset, a toggle, a context and the `expect`ed
result. The server, the Go SDK and the TypeScript SDK all run them. A new SDK is finished when it passes them.

## Remote evaluation, and what it sees

A client key never receives rules: the server evaluates for the context it is sent and returns values. That context
is not stored, logged or put in the Logbook. Send only the attributes your rules look at.
