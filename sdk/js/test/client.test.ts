import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer, type Server, type ServerResponse } from "node:http";
import { mkdtempSync, existsSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { AddressInfo } from "node:net";
import { ToggleClient, trimTrailingSlashes } from "../src/client.js";
import { WebClient } from "../src/web.js";
import type { Ruleset } from "../src/evaluate.js";

class Fake {
  on = false;
  version = 1;
  lastEventId = "";
  telemetry: string[] = [];
  rulesets = 0;
  streams = new Set<ServerResponse>();
  server: Server;
  url = "";
  constructor() {
    this.server = createServer((req, res) => {
      if (req.headers.authorization !== "Bearer lwt_srv_test" && !req.url?.startsWith("/api/sdk/v1/evaluate")) { res.writeHead(401).end(); return; }
      const url = req.url ?? "";
      if (url === "/api/sdk/v1/ruleset") {
        this.rulesets++;
        if (req.headers["if-none-match"] === this.etag()) { res.writeHead(304).end(); return; }
        res.writeHead(200, { ETag: this.etag(), "Content-Type": "application/json" }).end(this.body());
      } else if (url === "/api/sdk/v1/stream") {
        this.lastEventId = String(req.headers["last-event-id"] ?? "");
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        this.streams.add(res);
        res.on("close", () => this.streams.delete(res));
        if (this.lastEventId !== this.id()) this.send(res);
      } else if (url === "/api/sdk/v1/telemetry") {
        let b = ""; req.on("data", (d) => (b += d)); req.on("end", () => { this.telemetry.push(b); res.writeHead(200).end("{}"); });
      } else if (url === "/api/sdk/v1/evaluate") {
        let b = ""; req.on("data", (d) => (b += d));
        req.on("end", () => {
          const ctx = JSON.parse(b).context ?? {};
          res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ environment: "production", values: { checkout: { value: ctx.plan === "pro", reason: "rule_match" }, label: { value: "b", reason: "fallthrough" } } }));
        });
      } else res.writeHead(404).end();
    });
  }
  id() { return `v${this.version}`; }
  etag() { return `"${this.id()}"`; }
  rs(): Ruleset {
    return { schema: 1, environment: "production", toggles: {
      checkout: { key: "checkout", type: "boolean", salt: "checkout", on: this.on, off_value: false, fallthrough: { value: true } },
      label: { key: "label", type: "string", salt: "label", on: true, off_value: "a", fallthrough: { value: "b" } },
    } };
  }
  body() { return JSON.stringify(this.rs()); }
  send(res: ServerResponse) { res.write(`id: ${this.id()}\nevent: ruleset\ndata: ${this.body()}\n\n`); }
  set(on: boolean) { this.on = on; this.version++; for (const s of this.streams) this.send(s); }
  async start() { await new Promise<void>((r) => this.server.listen(0, "127.0.0.1", r)); this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`; return this; }
  async stop() { for (const s of this.streams) s.end(); this.server.closeAllConnections(); await new Promise((r) => this.server.close(r)); }
}

async function until(what: string, ok: () => boolean): Promise<void> {
  for (let i = 0; i < 200; i++) { if (ok()) return; await new Promise((r) => setTimeout(r, 20)); }
  assert.fail(`timed out waiting for ${what}`);
}

test("the client streams changes and evaluates locally", async () => {
  const f = await new Fake().start();
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_test", reportEvery: -1 });
  try {
    assert.equal(c.ready, true);
    assert.equal(c.bool("checkout", { key: "u" }, true), false);
    assert.equal(c.boolDetail("checkout", {}, true).reason, "off");
    f.set(true);
    await until("the change on the stream", () => c.bool("checkout", {}, false));
    assert.equal(c.string("label", {}, "x"), "b");
    assert.equal(c.bool("nope", {}, true), true);
    assert.equal(c.boolDetail("nope", {}, true).reason, "not_found");
    assert.deepEqual(c.boolDetail("label", {}, true), { value: true, reason: "type_mismatch" });
  } finally { await c.close(); await f.stop(); }
});

test("the client polls with the ETag when not streaming", async () => {
  const f = await new Fake().start();
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_test", noStream: true, pollEvery: 30, reportEvery: -1 });
  try {
    f.set(true);
    await until("a poll to see the change", () => c.bool("checkout", {}, false));
    assert.ok(f.rulesets >= 2);
  } finally { await c.close(); await f.stop(); }
});

test("a snapshot file keeps the last rules when the server is gone, and offline use needs only the file", async () => {
  const f = await new Fake().start();
  f.on = true;
  const path = join(mkdtempSync(join(tmpdir(), "lwt-")), "toggles.json");
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_test", snapshotFile: path, reportEvery: -1 });
  await until("the snapshot", () => existsSync(path));
  await c.close();
  const url = f.url;
  await f.stop();
  const c2 = await ToggleClient.connect({ serverUrl: url, key: "lwt_srv_test", snapshotFile: path, initTimeout: 300, reportEvery: -1, onError: () => undefined });
  assert.equal(c2.bool("checkout", {}, false), true);
  await c2.close();
  assert.equal(ToggleClient.fromFile(path).bool("checkout", {}, false), true);
  assert.ok(JSON.parse(readFileSync(path, "utf8")).toggles.checkout);
  // With nothing at all: the defaults, no error.
  const c3 = await ToggleClient.connect({ serverUrl: url, key: "lwt_srv_test", initTimeout: 100, reportEvery: -1, onError: () => undefined });
  assert.equal(c3.ready, false);
  assert.equal(c3.bool("checkout", {}, true), true);
  assert.equal(c3.boolDetail("checkout", {}, true).reason, "not_ready");
  await c3.close();
});

test("the stream resumes with the last event id", async () => {
  const f = await new Fake().start();
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_test", reportEvery: -1, pollEvery: 50, onError: () => undefined });
  try {
    f.set(true);
    await until("the change", () => c.bool("checkout", {}, false));
    f.lastEventId = "";
    for (const s of [...f.streams]) s.destroy();
    await until("a reconnect that says where it was", () => f.lastEventId === "v2");
  } finally { await c.close(); await f.stop(); }
});

test("counts are reported without any context", async () => {
  const f = await new Fake().start();
  f.on = true;
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_test", reportEvery: 40 });
  try {
    for (let i = 0; i < 5; i++) c.bool("checkout", { key: `user-${i}`, email: "someone@example.com" }, false);
    await until("a report", () => f.telemetry.length > 0);
    const all = f.telemetry.join(" ");
    assert.ok(all.includes("checkout"));
    assert.ok(!all.includes("user-") && !all.includes("example.com"), all);
  } finally { await c.close(); await f.stop(); }
});

test("a refused key is reported to onError", async () => {
  const f = await new Fake().start();
  const errors: Error[] = [];
  const c = await ToggleClient.connect({ serverUrl: f.url, key: "lwt_srv_wrong", initTimeout: 100, reportEvery: -1, onError: (e) => errors.push(e) });
  try { await until("an error", () => errors.length > 0); } finally { await c.close(); await f.stop(); }
});

test("the browser client asks for values for its context and says when they change", async () => {
  const f = await new Fake().start();
  const w = new WebClient({ serverUrl: f.url, clientKey: "lwt_cli_x", context: { key: "u1", plan: "free" } });
  try {
    assert.equal(w.bool("checkout", true), true); // not ready: the default
    assert.equal(w.boolDetail("checkout", true).reason, "not_ready");
    await w.init();
    assert.equal(w.bool("checkout", true), false);
    let changed = 0;
    w.onChange(() => changed++);
    await w.setContext({ key: "u1", plan: "pro" });
    assert.equal(w.bool("checkout", false), true);
    assert.equal(changed, 1);
    assert.equal(w.string("label", "x"), "b");
    assert.equal(w.bool("unknown", true), true);
    assert.equal(w.boolDetail("label", true).reason, "type_mismatch");
  } finally { w.close(); await f.stop(); }
});

test("trimTrailingSlashes trims in one pass, however long the run", () => {
  assert.equal(trimTrailingSlashes("https://x.example///"), "https://x.example");
  assert.equal(trimTrailingSlashes("https://x.example/a"), "https://x.example/a");
  const long = "https://x.example" + "/".repeat(200000) + "y";
  const t = Date.now();
  assert.equal(trimTrailingSlashes(long), long);
  assert.ok(Date.now() - t < 200);
});
