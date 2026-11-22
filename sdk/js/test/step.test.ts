import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Step, environ, Failed } from "../src/step.js";

function setup(inputs: Record<string, unknown>): { dir: string } {
  const dir = mkdtempSync(join(tmpdir(), "lwstep-"));
  writeFileSync(join(dir, "inputs.json"), JSON.stringify({ contract: 1, inputs }));
  process.env.LAUNCHWAY_STEP_INPUTS = join(dir, "inputs.json");
  process.env.LAUNCHWAY_STEP_OUTPUTS = join(dir, "outputs.json");
  process.env.LAUNCHWAY_STEP_RESULTS = join(dir, "results.ndjson");
  writeFileSync(process.env.LAUNCHWAY_STEP_RESULTS, "");
  return { dir };
}

test("inputs arrive typed, outputs and results are written to the contract's files", () => {
  const { dir } = setup({ who: "Ada", n: 3, on: true, files: ["a", "b"], meta: { k: 1 }, token: { secret: "LW_TEST_TOKEN" } });
  process.env.LW_TEST_TOKEN = "s3cr3t-value";
  const s = new Step();
  assert.equal(s.string("who"), "Ada");
  assert.equal(s.number("n"), 3);
  assert.equal(s.boolean("on"), true);
  assert.deepEqual(s.list("files"), ["a", "b"]);
  assert.deepEqual(s.record("meta"), { k: 1 });
  assert.equal(s.secret("token"), "s3cr3t-value");
  assert.equal(s.string("missing"), "");
  s.output("greeting", "hi");
  s.output("count", 2);
  s.summary("**done**");
  s.annotate("warning", "a.txt", 3, "look");
  s.finding("R1", "high", "a.txt", 3, "bad");
  s.test("t1", "passed", 12.4);
  s.mask("hunter2-value");
  s.writeOutputs();
  assert.deepEqual(JSON.parse(readFileSync(join(dir, "outputs.json"), "utf8")), { greeting: "hi", count: 2 });
  const lines = readFileSync(join(dir, "results.ndjson"), "utf8").trim().split("\n").map((l) => JSON.parse(l));
  assert.deepEqual(lines.map((l) => l.t), ["summary", "annotation", "finding", "test", "mask"]);
  assert.equal(lines[3].duration_ms, 12);
});

test("a wrong type is a failure, not a silent default", () => {
  setup({ who: 5 });
  assert.throws(() => new Step().string("who"), Failed);
});

test("a tool the step starts does not get the variables that let it write results", () => {
  setup({});
  const e = environ();
  assert.equal(e.LAUNCHWAY_STEP_RESULTS, undefined);
  assert.equal(e.LAUNCHWAY_STEP_OUTPUTS, undefined);
  assert.ok(e.LAUNCHWAY_STEP_INPUTS);
});
