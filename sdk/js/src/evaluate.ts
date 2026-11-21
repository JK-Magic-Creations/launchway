// The evaluator: the same rules as the server's (internal/toggles) and the Go
// SDK's, run against the shared vectors in sdk/vectors/evaluation.json.
import { sha256 } from "./sha256.js";

export type ValueType = "boolean" | "string" | "number" | "json";
export type Value = unknown;
/** What an application says about who is asking: `key` (what percentages are taken over) and attributes. */
export type Context = Record<string, unknown>;

export interface Clause { attribute?: string; op: string; values?: unknown[]; negate?: boolean }
export interface Share { value: Value; weight: number }
export interface Rollout { by?: string; buckets: Share[] }
export interface Serve { value?: Value; rollout?: Rollout }
export interface Rule { id?: string; description?: string; clauses?: Clause[]; serve: Serve }
export interface Prerequisite { toggle: string; value: Value }
export interface Flag {
  key: string; type: ValueType; kind?: string; client_visible?: boolean; salt: string;
  on: boolean; off_value: Value; prerequisites?: Prerequisite[]; rules?: Rule[]; fallthrough: Serve;
}
export interface Segment { description?: string; included?: string[]; excluded?: string[]; rules?: { clauses: Clause[] }[] }
export interface Ruleset { schema: number; environment: string; toggles: Record<string, Flag>; segments?: Record<string, Segment> }
export interface Result { value: Value; reason: string; rule_id?: string }

export const SCHEMA_VERSION = 1;
const MAX_DEPTH = 10;
const BUCKETS = 10000;

/** The bucket, 0 to 9999, of a value for a toggle's salt: SHA-256 of `salt + "." + value`, first four bytes big-endian, modulo 10000. */
export function bucket(salt: string, value: string): number {
  const h = sha256(salt + "." + value);
  return (((h[0]! << 24) | (h[1]! << 16) | (h[2]! << 8) | h[3]!) >>> 0) % BUCKETS;
}

function bucketText(v: unknown): string | undefined {
  if (typeof v === "string") return v;
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "number" && Number.isFinite(v)) {
    if (Number.isInteger(v) && Math.abs(v) < 1e15) return String(v);
    return shortest(v);
  }
  return undefined;
}

// JavaScript's number-to-text is the shortest that reads back the same, as Go's 'g' with -1 precision, apart from the exponent's form.
function shortest(v: number): string {
  let s = String(v);
  const m = /^(-?[\d.]+)e([+-])(\d+)$/.exec(s);
  if (m) s = `${m[1]}e${m[2]}${m[3]!.padStart(2, "0")}`;
  return s;
}

function same(a: unknown, b: unknown): boolean {
  return typeof a === typeof b && (typeof a === "string" || typeof a === "number" || typeof a === "boolean") && a === b;
}

interface Semver { n: [number, number, number]; pre: string[] }

function parseSemver(input: string): Semver | undefined {
  let s = input.startsWith("v") ? input.slice(1) : input;
  const plus = s.indexOf("+");
  if (plus >= 0) s = s.slice(0, plus);
  let pre: string[] = [];
  const dash = s.indexOf("-");
  if (dash >= 0) {
    pre = s.slice(dash + 1).split(".");
    if (pre.some((p) => p === "")) return undefined;
    s = s.slice(0, dash);
  }
  const parts = s.split(".");
  if (parts.length !== 3) return undefined;
  const n: number[] = [];
  for (const p of parts) {
    if (!/^(0|[1-9]\d*)$/.test(p)) return undefined;
    n.push(Number(p));
  }
  return { n: n as [number, number, number], pre };
}

/** Orders two semantic versions (semver.org precedence); undefined when either is not one. */
export function compareSemver(a: string, b: string): number | undefined {
  const x = parseSemver(a), y = parseSemver(b);
  if (!x || !y) return undefined;
  for (let i = 0; i < 3; i++) if (x.n[i] !== y.n[i]) return x.n[i]! < y.n[i]! ? -1 : 1;
  if (x.pre.length === 0 && y.pre.length === 0) return 0;
  if (x.pre.length === 0) return 1;
  if (y.pre.length === 0) return -1;
  for (let i = 0; i < x.pre.length && i < y.pre.length; i++) {
    const p = x.pre[i]!, q = y.pre[i]!;
    const pn = /^\d+$/.test(p), qn = /^\d+$/.test(q);
    if (pn && qn) { if (Number(p) !== Number(q)) return Number(p) < Number(q) ? -1 : 1; }
    else if (pn) return -1;
    else if (qn) return 1;
    else if (p !== q) return p < q ? -1 : 1;
  }
  if (x.pre.length !== y.pre.length) return x.pre.length < y.pre.length ? -1 : 1;
  return 0;
}

function matchOne(op: string, have: unknown, values: unknown[]): boolean {
  switch (op) {
    case "equals": return values.length > 0 && same(have, values[0]);
    case "in": return values.some((v) => same(have, v));
    case "prefix": case "suffix": case "contains": {
      if (typeof have !== "string") return false;
      return values.some((v) => typeof v === "string" && (op === "prefix" ? have.startsWith(v) : op === "suffix" ? have.endsWith(v) : have.includes(v)));
    }
    case "number_eq": case "number_lt": case "number_lte": case "number_gt": case "number_gte": {
      const b = values[0];
      if (typeof have !== "number" || typeof b !== "number") return false;
      return op === "number_eq" ? have === b : op === "number_lt" ? have < b : op === "number_lte" ? have <= b : op === "number_gt" ? have > b : have >= b;
    }
    case "semver_eq": case "semver_lt": case "semver_lte": case "semver_gt": case "semver_gte": {
      const b = values[0];
      if (typeof have !== "string" || typeof b !== "string") return false;
      const c = compareSemver(have, b);
      if (c === undefined) return false;
      return op === "semver_eq" ? c === 0 : op === "semver_lt" ? c < 0 : op === "semver_lte" ? c <= 0 : op === "semver_gt" ? c > 0 : c >= 0;
    }
  }
  return false;
}

function clauseTrue(c: Clause, ctx: Context, segments: Record<string, Segment>, depth: number): boolean {
  const values = c.values ?? [];
  if (c.op === "in_segment") return values.some((n) => typeof n === "string" && segments[n] !== undefined && inSegment(segments[n]!, ctx, segments, depth));
  const have = c.attribute === undefined ? undefined : ctx[c.attribute];
  if (have === undefined || have === null) return false;
  if (c.op === "exists") return true;
  if (Array.isArray(have)) return have.some((el) => matchOne(c.op, el, values));
  return matchOne(c.op, have, values);
}

function matchClause(c: Clause, ctx: Context, segments: Record<string, Segment>, depth: number): boolean {
  return clauseTrue(c, ctx, segments, depth) !== (c.negate === true);
}

function allMatch(cs: Clause[] | undefined, ctx: Context, segments: Record<string, Segment>, depth: number): boolean {
  return (cs ?? []).every((c) => matchClause(c, ctx, segments, depth));
}

function inSegment(seg: Segment, ctx: Context, segments: Record<string, Segment>, depth: number): boolean {
  if (depth > 5) return false;
  const key = bucketText(ctx["key"]) ?? "";
  if (key !== "" && (seg.excluded ?? []).includes(key)) return false;
  if (key !== "" && (seg.included ?? []).includes(key)) return true;
  return (seg.rules ?? []).some((r) => allMatch(r.clauses, ctx, segments, depth + 1));
}

function serve(f: Flag, s: Serve, ctx: Context): { value: Value; reason?: string } {
  if (!s.rollout) return { value: s.value };
  const text = bucketText(ctx[s.rollout.by || "key"]);
  if (text === undefined || text === "") return { value: f.off_value, reason: "missing_bucket_key" };
  const b = bucket(f.salt, text);
  let edge = 0;
  for (const share of s.rollout.buckets) {
    edge += share.weight;
    if (b < edge) return { value: share.value };
  }
  const last = s.rollout.buckets[s.rollout.buckets.length - 1];
  return { value: last ? last.value : null };
}

function evaluateAt(rs: Ruleset, key: string, ctx: Context, depth: number): Result {
  const f = rs.toggles[key];
  if (!f) return { value: null, reason: "not_found" };
  if (depth > MAX_DEPTH) return { value: f.off_value, reason: "error" };
  if (!f.on) return { value: f.off_value, reason: "off" };
  for (const p of f.prerequisites ?? []) {
    const got = evaluateAt(rs, p.toggle, ctx, depth + 1);
    if (got.reason === "not_found" || got.reason === "error" || !same(got.value, p.value)) return { value: f.off_value, reason: "prerequisite_failed" };
  }
  const segments = rs.segments ?? {};
  for (const r of f.rules ?? []) {
    if (allMatch(r.clauses, ctx, segments, 0)) {
      const s = serve(f, r.serve, ctx);
      const out: Result = { value: s.value, reason: s.reason ?? "rule_match" };
      if (r.id) out.rule_id = r.id;
      return out;
    }
  }
  const s = serve(f, f.fallthrough, ctx);
  return { value: s.value, reason: s.reason ?? "fallthrough" };
}

/** The value of a toggle for a context. A toggle that is not in the ruleset has the reason `not_found`: the caller's default applies. */
export function evaluate(rs: Ruleset, key: string, ctx: Context = {}): Result {
  return evaluateAt(rs, key, ctx, 0);
}
