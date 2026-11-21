// The browser client: asks the server for the values of the toggles that are
// visible to client applications, for one context. It holds a client key
// (lwt_cli_...), which is public by nature and can only ask for values: it
// never receives a rule, a segment or anyone else's data. The context goes to
// your Launchway server (over TLS) and nowhere else.
import type { Context } from "./evaluate.js";

export interface WebOptions {
  serverUrl: string;
  /** A client SDK key (lwt_cli_...). */
  clientKey: string;
  /** Who is asking: `key` and attributes. Send only what your rules look at. */
  context: Context;
  /** Milliseconds between refreshes (default: not at all; call refresh()). */
  refreshEvery?: number;
  fetch?: typeof fetch;
  onError?: (err: Error) => void;
}

export interface WebDetail<T> { value: T; reason: string }

export class WebClient {
  private values: Record<string, { value: unknown; reason: string }> = {};
  private listeners = new Set<() => void>();
  private timer: ReturnType<typeof setInterval> | undefined;
  private ctx: Context;
  private loaded = false;

  constructor(private readonly o: WebOptions) {
    this.ctx = o.context;
  }

  /** Fetches the values once; resolves when they are in (or the attempt failed: defaults then apply). */
  async init(): Promise<void> {
    await this.refresh();
    if (this.o.refreshEvery && this.o.refreshEvery > 0 && !this.timer) {
      this.timer = setInterval(() => { void this.refresh(); }, this.o.refreshEvery);
    }
  }

  get ready(): boolean { return this.loaded; }

  /** Changes who is asking (a user signs in) and fetches again. */
  async setContext(ctx: Context): Promise<void> {
    this.ctx = ctx;
    await this.refresh();
  }

  /** Calls `fn` whenever the values change. Returns a function that stops it. */
  onChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  async refresh(): Promise<void> {
    const f = this.o.fetch ?? fetch;
    try {
      const res = await f(this.o.serverUrl.replace(/\/+$/, "") + "/api/sdk/v1/evaluate", {
        method: "POST",
        headers: { Authorization: `Bearer ${this.o.clientKey}`, "Content-Type": "application/json" },
        body: JSON.stringify({ context: this.ctx }),
      });
      if (!res.ok) throw new Error(`toggles: the server answered ${res.status}`);
      const body = (await res.json()) as { values: Record<string, { value: unknown; reason: string }> };
      const changed = JSON.stringify(body.values) !== JSON.stringify(this.values);
      this.values = body.values;
      this.loaded = true;
      if (changed) this.listeners.forEach((fn) => fn());
    } catch (e) {
      this.o.onError?.(e instanceof Error ? e : new Error(String(e)));
    }
  }

  close(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  private detail<T>(key: string, def: T, ok: (v: unknown) => v is T): WebDetail<T> {
    const e = this.values[key];
    if (!this.loaded) return { value: def, reason: "not_ready" };
    if (!e || e.reason === "not_found") return { value: def, reason: "not_found" };
    return ok(e.value) ? { value: e.value, reason: e.reason } : { value: def, reason: "type_mismatch" };
  }

  boolDetail(key: string, def: boolean): WebDetail<boolean> { return this.detail(key, def, (v): v is boolean => typeof v === "boolean"); }
  bool(key: string, def: boolean): boolean { return this.boolDetail(key, def).value; }
  stringDetail(key: string, def: string): WebDetail<string> { return this.detail(key, def, (v): v is string => typeof v === "string"); }
  string(key: string, def: string): string { return this.stringDetail(key, def).value; }
  numberDetail(key: string, def: number): WebDetail<number> { return this.detail(key, def, (v): v is number => typeof v === "number"); }
  number(key: string, def: number): number { return this.numberDetail(key, def).value; }
  json<T = unknown>(key: string, def: T): T { return this.detail<T>(key, def, (_v): _v is T => true).value; }
}
