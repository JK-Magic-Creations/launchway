import { test } from "node:test";
import assert from "node:assert/strict";
import { OpenFeature } from "@openfeature/server-sdk";
import { ToggleClient } from "../src/client.js";
import { LaunchwayServerProvider } from "../src/openfeature.js";
import type { Ruleset } from "../src/evaluate.js";

const rs: Ruleset = { schema: 1, environment: "x", toggles: {
  checkout: { key: "checkout", type: "boolean", salt: "checkout", on: true, off_value: false, fallthrough: { value: false },
    rules: [{ id: "pro", clauses: [{ attribute: "plan", op: "equals", values: ["pro"] }], serve: { value: true } }] },
  limit: { key: "limit", type: "number", salt: "limit", on: true, off_value: 0, fallthrough: { value: 5 } },
  rollout: { key: "rollout", type: "boolean", salt: "rollout", on: true, off_value: false, fallthrough: { rollout: { buckets: [{ value: true, weight: 5000 }, { value: false, weight: 5000 }] } } },
} };

test("the provider works through the OpenFeature client", async () => {
  await OpenFeature.setProviderAndWait(new LaunchwayServerProvider(ToggleClient.fromRuleset(rs)));
  const c = OpenFeature.getClient();
  assert.equal(await c.getBooleanValue("checkout", false, { targetingKey: "u1", plan: "pro" }), true);
  const d = await c.getBooleanDetails("checkout", true, { targetingKey: "u1", plan: "free" });
  assert.equal(d.value, false);
  assert.equal(d.reason, "DEFAULT");
  assert.equal(await c.getNumberValue("limit", 1, { targetingKey: "u1" }), 5);
  const missing = await c.getBooleanDetails("nope", true, { targetingKey: "u1" });
  assert.equal(missing.value, true);
  assert.equal(missing.errorCode, "FLAG_NOT_FOUND");
  const wrong = await c.getStringDetails("checkout", "d", { targetingKey: "u1" });
  assert.equal(wrong.errorCode, "TYPE_MISMATCH");
  const nokey = await c.getBooleanDetails("rollout", false, {});
  assert.equal(nokey.errorCode, "TARGETING_KEY_MISSING");
});
