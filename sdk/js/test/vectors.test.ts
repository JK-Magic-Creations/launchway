import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { bucket, evaluate, type Ruleset, type Context, type Result } from "../src/evaluate.js";

interface Vectors {
  hashes: { salt: string; value: string; bucket: number }[];
  cases: { name: string; ruleset: Ruleset; toggle: string; context: Context; expect: Result }[];
}
const vectors: Vectors = JSON.parse(readFileSync(new URL("../../../vectors/evaluation.json", import.meta.url), "utf8"));

test("the bucketing hash gives the shared vectors' buckets", () => {
  assert.ok(vectors.hashes.length > 0);
  for (const h of vectors.hashes) assert.equal(bucket(h.salt, h.value), h.bucket, `${h.salt} ${h.value}`);
});

test("the evaluator gives every shared vector's answer", () => {
  assert.ok(vectors.cases.length >= 60);
  for (const c of vectors.cases) {
    const got = evaluate(c.ruleset, c.toggle, c.context);
    assert.deepEqual(got, c.expect, c.name);
  }
});
