# Toggle SDKs

Small libraries that read [feature toggles](feature-toggles.md). The servers evaluate locally: a toggle costs no
network call, keeps working when Launchway cannot be reached, and nothing about your users leaves the application.
Both libraries pass the same [test vectors](../reference/toggles-protocol.md) as the server. They are in the
repository (`sdk/go/toggles`, `sdk/js`); nothing is published to a package registry yet, so use them from a checkout
(a Go `replace` directive, or `npm install ./sdk/js`).

## Go

```go
import toggles "github.com/jk-magic-creations/launchway/sdk/go/toggles"

c, _ := toggles.New(ctx, toggles.Options{
    ServerURL:    "https://launchway.example.com",
    Key:          os.Getenv("LAUNCHWAY_TOGGLES_KEY"), // a server key
    SnapshotFile: "/var/lib/app/toggles.json",        // the last rules, for a restart while Launchway is down
})
defer c.Close()

if c.Bool("checkout-v2", toggles.Context{"key": user.ID, "plan": user.Plan}, false) {
    // the new checkout
}
```

`New` waits up to three seconds for the first ruleset and then returns what it has: the snapshot file, or nothing,
in which case every toggle answers with your default (`BoolDetail` says `not_ready`). It keeps a stream open
(reconnecting with the last event id), polls with the ETag while the stream is down, writes the snapshot file after
every change, and sends counts of evaluations once a minute (`ReportEvery: -1` stops it). `OnError` hears of an
unreachable server or a refused key. Types: `Bool`, `String`, `Number`, `JSON`, each with a `...Detail` that returns the
reason and the rule; a toggle of another type answers your default with the reason `type_mismatch`.

**Offline and tests.** `toggles.NewFromFile(path)` reads a snapshot file alone. In tests,
`toggletest.Values(map[string]any{"checkout-v2": true})` makes a client with no server, and `toggletest.Rules` takes a
full ruleset.

**OpenFeature.** `sdk/go/toggles/openfeature` (its own module) is an [OpenFeature](https://openfeature.dev)
provider over the client: `openfeature.SetProviderAndWait(lwopenfeature.New(c))`, then
`openfeature.NewClient("app").BooleanValue(ctx, "checkout-v2", false, openfeature.NewEvaluationContext("user-1", attrs))`.
The targeting key is the context key.

## TypeScript (Node)

```ts
import { ToggleClient } from "@launchway/toggles";

const toggles = await ToggleClient.connect({
  serverUrl: "https://launchway.example.com",
  key: process.env.LAUNCHWAY_TOGGLES_KEY!,   // a server key: a secret, never in a web page
  snapshotFile: "/var/lib/app/toggles.json",
});
if (toggles.bool("checkout-v2", { key: user.id, plan: user.plan }, false)) { /* ... */ }
await toggles.close();
```

The same behavior as the Go client (`bool`, `string`, `number`, `json` and their `...Detail`, stream with resume,
polling, snapshot file, counts, `ToggleClient.fromFile`, `ToggleClient.fromRuleset` for tests).

## TypeScript (browser)

A browser never gets rules. It holds a **client key** (public) and asks for the values for its context:

```ts
import { WebClient } from "@launchway/toggles/web";

const toggles = new WebClient({ serverUrl: "https://launchway.example.com", clientKey: "lwt_cli_...", context: { key: user.id } });
await toggles.init();
if (toggles.bool("new-banner", false)) { /* ... */ }
toggles.onChange(() => rerender());
await toggles.setContext({ key: user.id, plan: "pro" });   // fetches again
```

Only toggles marked **visible to client applications** are returned, and only from the websites the key names.

## OpenFeature (TypeScript)

`@launchway/toggles/openfeature` has `LaunchwayServerProvider` (for `@openfeature/server-sdk`, over a `ToggleClient`)
and `LaunchwayWebProvider` (for `@openfeature/web-sdk`, over a `WebClient`):

```ts
import { OpenFeature } from "@openfeature/server-sdk";
import { LaunchwayServerProvider } from "@launchway/toggles/openfeature";
await OpenFeature.setProviderAndWait(new LaunchwayServerProvider(toggles));
const on = await OpenFeature.getClient().getBooleanValue("checkout-v2", false, { targetingKey: user.id, plan: user.plan });
```

Providers for Java, Python and .NET are coming. Any language can speak the [protocol](../reference/toggles-protocol.md) and pass the vectors.

## Building and testing the SDKs

```
cd sdk/go/toggles && go test ./...            # the evaluator, the client, the test helper
cd sdk/go/toggles/openfeature && go test ./...
cd sdk/js && npm ci && npm test               # builds and runs the tests with Node's own runner
```

`make test-sdks` runs all three.
