// The Node client: holds an environment's ruleset in memory, kept current by a
// stream, and evaluates locally. Use it with a server key (lwt_srv_...), which
// is a secret: never put it in a web page (a browser uses `web.ts` with a
// client key).
import { readFileSync, writeFileSync, renameSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { evaluate, SCHEMA_VERSION, type Context, type Ruleset, type Result, type ValueType } from "./evaluate.js";

export interface Options {
  /** The Launchway server, such as https://launchway.example.com. Empty: no server; only `snapshotFile`. */
  serverUrl?: string;
  /** A server SDK key (lwt_srv_...). */
  key?: string;
  /** Where the ruleset is kept on disk after each change, and read from at start when the server cannot be reached. */
  snapshotFile?: string;
  /** Poll instead of keeping a stream open. */
  noStream?: boolean;
  /** Milliseconds between polls when not streaming, and while a stream is down (default 30000). */
  pollEvery?: number;
  /** Milliseconds to wait for the first ruleset (default 3000). */
  initTimeout?: number;
  /** Milliseconds between reports of evaluation counts (default 60000; negative: never). Counts hold toggle, value and reason, never the context. */
  reportEvery?: number;
  /** A fetch to use instead of the global one. */
  fetch?: typeof fetch;
  /** Hears of problems: an unreachable server, a refused key. */
  onError?: (err: Error) => void;
}

export interface Detail<T> { value: T; reason: string; ruleId?: string }

export class ToggleClient {
  private rs: Ruleset | undefined;
  private etag = "";
  private abort = new AbortController();
  private running: Promise<void> | undefined;
  private counts = new Map<string, number>();
  private readonly o: Required<Pick<Options, "pollEvery" | "initTimeout" | "reportEvery">> & Options;
  private readyResolve!: () => void;
  private readonly readyPromise = new Promise<void>((r) => { this.readyResolve = r; });
  private readonly fetchFn: typeof fetch;

  private constructor(o: Options) {
    this.o = { pollEvery: 30000, initTimeout: 3000, reportEvery: 60000, ...o };
    this.fetchFn = o.fetch ?? fetch;
  }

  /** Starts a client; resolves when it has a ruleset, or after `initTimeout`. */
  static async connect(o: Options): Promise<ToggleClient> {
    if (!o.serverUrl && !o.snapshotFile) throw new Error("toggles: give a serverUrl and key, or a snapshotFile");
    if (o.serverUrl && !o.key) throw new Error("toggles: a key is needed with a serverUrl");
    const c = new ToggleClient(o);
    if (!o.serverUrl) {
      c.loadSnapshot();
      if (!c.rs) throw new Error("toggles: no usable snapshot file");
      return c;
    }
    c.running = c.run();
    let timer: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([c.readyPromise, new Promise<void>((r) => { timer = setTimeout(r, c.o.initTimeout); })]);
    if (timer) clearTimeout(timer);
    if (!c.rs) c.loadSnapshot();
    return c;
  }

  /** A client that holds a ruleset and talks to nobody: for tests. */
  static fromRuleset(rs: Ruleset): ToggleClient {
    const c = new ToggleClient({});
    c.rs = rs;
    c.readyResolve();
    return c;
  }

  /** A client over a snapshot file alone. */
  static fromFile(path: string): ToggleClient {
    const c = new ToggleClient({ snapshotFile: path });
    c.loadSnapshot();
    if (!c.rs) throw new Error(`toggles: ${path} is not a usable snapshot file`);
    return c;
  }

  get ready(): boolean { return this.rs !== undefined; }
  get ruleset(): Ruleset | undefined { return this.rs; }

  /** Stops the client, reporting the counts still held. */
  async close(): Promise<void> {
    this.abort.abort();
    await this.running;
    await this.report().catch(() => undefined);
  }

  private fail(err: unknown): void {
    this.o.onError?.(err instanceof Error ? err : new Error(String(err)));
  }

  private loadSnapshot(): void {
    if (!this.o.snapshotFile || this.rs) return;
    try {
      const rs = JSON.parse(readFileSync(this.o.snapshotFile, "utf8")) as Ruleset;
      if (rs && rs.toggles) { this.rs = rs; this.readyResolve(); }
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "ENOENT") this.fail(e);
    }
  }

  private store(body: string, etag: string): void {
    const rs = JSON.parse(body) as Ruleset;
    if (rs.schema > SCHEMA_VERSION) throw new Error(`toggles: the server speaks ruleset schema ${rs.schema}, newer than this SDK's ${SCHEMA_VERSION}: upgrade the SDK`);
    this.rs = rs;
    this.etag = etag;
    this.readyResolve();
    if (this.o.snapshotFile) {
      try {
        mkdirSync(dirname(this.o.snapshotFile), { recursive: true });
        writeFileSync(this.o.snapshotFile + ".tmp", body, { mode: 0o600 });
        renameSync(this.o.snapshotFile + ".tmp", this.o.snapshotFile);
      } catch (e) { this.fail(e); }
    }
  }

  private url(path: string): string { return this.o.serverUrl!.replace(/\/+$/, "") + path; }
  private headers(extra: Record<string, string> = {}): Record<string, string> {
    return { Authorization: `Bearer ${this.o.key}`, "User-Agent": "launchway-js-sdk", ...extra };
  }

  private async run(): Promise<void> {
    const sig = this.abort.signal;
    let reporter: ReturnType<typeof setInterval> | undefined;
    if (this.o.reportEvery > 0) {
      reporter = setInterval(() => { this.report().catch((e) => this.fail(e)); }, this.o.reportEvery);
      reporter.unref();
    }
    let backoff = 1000;
    while (!sig.aborted) {
      try {
        if (this.o.noStream) { await this.poll(); await this.sleep(this.o.pollEvery); continue; }
        await this.stream();
      } catch (e) {
        if (sig.aborted) break;
        this.fail(e);
      }
      if (sig.aborted) break;
      await this.poll().catch(() => undefined);
      await this.sleep(Math.min(backoff, this.o.pollEvery) * (0.5 + Math.random() / 2));
      backoff = Math.min(backoff * 2, 60000);
    }
    if (reporter) clearInterval(reporter);
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const t = setTimeout(resolve, ms);
      this.abort.signal.addEventListener("abort", () => { clearTimeout(t); resolve(); }, { once: true });
    });
  }

  private async poll(): Promise<void> {
    const res = await this.fetchFn(this.url("/api/sdk/v1/ruleset"), { headers: this.headers(this.etag ? { "If-None-Match": this.etag } : {}), signal: this.abort.signal });
    if (res.status === 304) return;
    if (res.status === 401 || res.status === 403) throw new Error(`toggles: the server refused the key (${res.status}): is it a current server key for this environment?`);
    if (!res.ok) throw new Error(`toggles: the server answered ${res.status}`);
    this.store(await res.text(), res.headers.get("ETag") ?? "");
  }

  private async stream(): Promise<void> {
    const headers = this.headers({ Accept: "text/event-stream" });
    if (this.etag) headers["Last-Event-ID"] = this.etag.replace(/"/g, "");
    const res = await this.fetchFn(this.url("/api/sdk/v1/stream"), { headers, signal: this.abort.signal });
    if (!res.ok || !res.body) throw new Error(`toggles: the stream answered ${res.status}`);
    const decoder = new TextDecoder();
    let buf = "", id = "", event = "", data = "";
    for await (const chunk of res.body as unknown as AsyncIterable<Uint8Array>) {
      buf += decoder.decode(chunk, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).replace(/\r$/, "");
        buf = buf.slice(nl + 1);
        if (line === "") {
          if (event === "ruleset" && data) {
            try { this.store(data, `"${id}"`); } catch (e) { this.fail(e); }
          }
          id = event = data = "";
        } else if (line.startsWith(":")) { /* a ping */ }
        else if (line.startsWith("id:")) id = line.slice(3).trim();
        else if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).replace(/^ /, "");
      }
    }
  }

  // ---- evaluating ----

  private detail(key: string, ctx: Context, want: ValueType): Result {
    if (!this.rs) return { value: undefined, reason: "not_ready" };
    const f = this.rs.toggles[key];
    if (!f) { this.count(key, null, "not_found"); return { value: undefined, reason: "not_found" }; }
    if (f.type !== want) return { value: undefined, reason: "type_mismatch" };
    const r = evaluate(this.rs, key, ctx);
    this.count(key, r.value, r.reason);
    return r;
  }

  private count(key: string, value: unknown, reason: string): void {
    let v = JSON.stringify(value ?? null);
    if (v.length > 64) v = v.slice(0, 61) + "...";
    const k = JSON.stringify([key, v, reason]);
    if (this.counts.size < 5000 || this.counts.has(k)) this.counts.set(k, (this.counts.get(k) ?? 0) + 1);
  }

  boolDetail(key: string, ctx: Context, def: boolean): Detail<boolean> {
    const r = this.detail(key, ctx, "boolean");
    return typeof r.value === "boolean" ? { value: r.value, reason: r.reason, ruleId: r.rule_id } : { value: def, reason: r.reason };
  }
  bool(key: string, ctx: Context, def: boolean): boolean { return this.boolDetail(key, ctx, def).value; }

  stringDetail(key: string, ctx: Context, def: string): Detail<string> {
    const r = this.detail(key, ctx, "string");
    return typeof r.value === "string" ? { value: r.value, reason: r.reason, ruleId: r.rule_id } : { value: def, reason: r.reason };
  }
  string(key: string, ctx: Context, def: string): string { return this.stringDetail(key, ctx, def).value; }

  numberDetail(key: string, ctx: Context, def: number): Detail<number> {
    const r = this.detail(key, ctx, "number");
    return typeof r.value === "number" ? { value: r.value, reason: r.reason, ruleId: r.rule_id } : { value: def, reason: r.reason };
  }
  number(key: string, ctx: Context, def: number): number { return this.numberDetail(key, ctx, def).value; }

  jsonDetail<T = unknown>(key: string, ctx: Context, def: T): Detail<T> {
    const r = this.detail(key, ctx, "json");
    if (r.reason === "not_ready" || r.reason === "not_found" || r.reason === "type_mismatch") return { value: def, reason: r.reason };
    return { value: r.value as T, reason: r.reason, ruleId: r.rule_id };
  }
  json<T = unknown>(key: string, ctx: Context, def: T): T { return this.jsonDetail(key, ctx, def).value; }

  // ---- counts ----

  /** Sends the counts held. A failure keeps them for the next try. */
  async report(): Promise<void> {
    if (!this.o.serverUrl || this.counts.size === 0 || this.o.reportEvery < 0) return;
    const held = this.counts;
    this.counts = new Map();
    const counts = [...held].map(([k, count]) => {
      const [toggle, value, reason] = JSON.parse(k) as [string, string, string];
      return { toggle, value: JSON.parse(value), reason, count };
    });
    try {
      const res = await this.fetchFn(this.url("/api/sdk/v1/telemetry"), { method: "POST", headers: this.headers({ "Content-Type": "application/json" }), body: JSON.stringify({ counts }) });
      if (!res.ok) throw new Error(`toggles: the server answered ${res.status} to the counts`);
    } catch (e) {
      for (const [k, n] of held) this.counts.set(k, (this.counts.get(k) ?? 0) + n);
      throw e;
    }
  }
}
