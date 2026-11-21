// OpenFeature providers (https://openfeature.dev) for Launchway: one for
// servers, over the local-evaluation client, and one for browsers, over the
// remote-evaluation client. Import from "@launchway/toggles/openfeature".
//
//   import { OpenFeature } from "@openfeature/server-sdk";
//   await OpenFeature.setProviderAndWait(new LaunchwayServerProvider(client));
//   const on = await OpenFeature.getClient().getBooleanValue("checkout-v2", false, { targetingKey: "user-1", plan: "pro" });
import { StandardResolutionReasons, ErrorCode, type EvaluationContext, type JsonValue, type Provider, type ResolutionDetails } from "@openfeature/server-sdk";
import type { Provider as WebProvider, ResolutionDetails as WebResolutionDetails, JsonValue as WebJsonValue } from "@openfeature/web-sdk";
import type { ToggleClient } from "./client.js";
import type { WebClient } from "./web.js";
import type { Context } from "./evaluate.js";

function contextOf(c: EvaluationContext): Context {
  const out: Context = {};
  for (const [k, v] of Object.entries(c)) out[k === "targetingKey" ? "key" : k] = v;
  return out;
}

function reasonOf(r: string): string {
  switch (r) {
    case "rule_match": return StandardResolutionReasons.TARGETING_MATCH;
    case "off": return StandardResolutionReasons.DISABLED;
    case "fallthrough": return StandardResolutionReasons.DEFAULT;
    default: return StandardResolutionReasons.ERROR;
  }
}

type Kind = "boolean" | "string" | "number" | "json";

export class LaunchwayServerProvider implements Provider {
  readonly metadata = { name: "launchway" } as const;
  readonly runsOn = "server" as const;
  constructor(private readonly client: ToggleClient) {}

  private resolve<T>(flag: string, def: T, ctx: EvaluationContext, kind: Kind): ResolutionDetails<T> {
    const rs = this.client.ruleset;
    if (!rs) return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.PROVIDER_NOT_READY, errorMessage: "no ruleset has been loaded yet" };
    const f = rs.toggles[flag];
    if (!f) return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.FLAG_NOT_FOUND, errorMessage: `no toggle ${flag}` };
    if (f.type !== kind) return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.TYPE_MISMATCH, errorMessage: `${flag} is a ${f.type} toggle` };
    const d = kind === "boolean" ? this.client.boolDetail(flag, contextOf(ctx), def as boolean)
      : kind === "string" ? this.client.stringDetail(flag, contextOf(ctx), def as string)
      : kind === "number" ? this.client.numberDetail(flag, contextOf(ctx), def as number)
      : this.client.jsonDetail(flag, contextOf(ctx), def);
    if (d.reason === "missing_bucket_key") return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.TARGETING_KEY_MISSING, errorMessage: "the context has no targeting key to take a percentage over" };
    return { value: d.value as T, reason: reasonOf(d.reason), ...(d.ruleId ? { variant: d.ruleId } : {}) };
  }

  async resolveBooleanEvaluation(flag: string, def: boolean, ctx: EvaluationContext): Promise<ResolutionDetails<boolean>> { return this.resolve(flag, def, ctx, "boolean"); }
  async resolveStringEvaluation(flag: string, def: string, ctx: EvaluationContext): Promise<ResolutionDetails<string>> { return this.resolve(flag, def, ctx, "string"); }
  async resolveNumberEvaluation(flag: string, def: number, ctx: EvaluationContext): Promise<ResolutionDetails<number>> { return this.resolve(flag, def, ctx, "number"); }
  async resolveObjectEvaluation<T extends JsonValue>(flag: string, def: T, ctx: EvaluationContext): Promise<ResolutionDetails<T>> { return this.resolve(flag, def, ctx, "json"); }
}

/** The browser provider: values are fetched for one context (see WebClient), so evaluation is a lookup. */
export class LaunchwayWebProvider implements WebProvider {
  readonly metadata = { name: "launchway" } as const;
  readonly runsOn = "client" as const;
  constructor(private readonly client: WebClient) {}

  async initialize(): Promise<void> { await this.client.init(); }
  async onContextChange(_old: unknown, ctx: EvaluationContext): Promise<void> { await this.client.setContext(contextOf(ctx)); }
  onClose(): Promise<void> { this.client.close(); return Promise.resolve(); }

  private wrap<T>(d: { value: T; reason: string }, def: T): WebResolutionDetails<T> {
    if (d.reason === "not_found") return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.FLAG_NOT_FOUND as never };
    if (d.reason === "type_mismatch") return { value: def, reason: StandardResolutionReasons.ERROR, errorCode: ErrorCode.TYPE_MISMATCH as never };
    return { value: d.value, reason: reasonOf(d.reason) };
  }
  resolveBooleanEvaluation(flag: string, def: boolean): WebResolutionDetails<boolean> { return this.wrap(this.client.boolDetail(flag, def), def); }
  resolveStringEvaluation(flag: string, def: string): WebResolutionDetails<string> { return this.wrap(this.client.stringDetail(flag, def), def); }
  resolveNumberEvaluation(flag: string, def: number): WebResolutionDetails<number> { return this.wrap(this.client.numberDetail(flag, def), def); }
  resolveObjectEvaluation<T extends WebJsonValue>(flag: string, def: T): WebResolutionDetails<T> { return { value: this.client.json(flag, def), reason: StandardResolutionReasons.DEFAULT }; }
}
