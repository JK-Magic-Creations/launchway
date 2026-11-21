import { call } from "./api";

// Variables at levels: a variable is set at a level, overridden by the same name at a nearer level, and can be
// locked from above. The server resolves them with one rule; these are its answers.

export type VarLevel = "installation" | "organization" | "unit" | "project";
export const VAR_LEVELS: VarLevel[] = ["installation", "organization", "unit", "project"];
export type VarType = "string" | "number" | "boolean" | "list" | "map" | "secret" | "connection";
export const VAR_TYPES: { id: VarType; label: string }[] = [
  { id: "string", label: "Text" }, { id: "number", label: "Number" }, { id: "boolean", label: "Yes or no" },
  { id: "list", label: "List" }, { id: "map", label: "Map" }, { id: "secret", label: "Secret reference" }, { id: "connection", label: "Connection" },
];

export interface VarSource { kind: "run" | "file" | "level" | "default"; level?: VarLevel; scope?: string; environment?: string }
export interface VarLink extends VarSource { value: unknown; display: string; locked?: boolean; overridden?: boolean; ignored?: string }
export interface VarLock { level: VarLevel; scope?: string; environment?: string; reason: string; by?: string; who: string }
export interface VarResolved {
  name: string; type: VarType; of?: string; description?: string; value: unknown; display: string; from: VarSource;
  chain: VarLink[]; locked?: VarLock; why: string;
}
export interface VarEntry extends VarResolved { set_here: boolean; can_override: boolean; blocked?: string }
export interface VarView {
  rule: string; level: VarLevel; scope?: string; project?: string; unit?: string; environment?: string;
  words: Record<string, { singular: string; plural: string }>; variables: VarEntry[];
}
export interface RecordedVar extends VarResolved { environment?: string; run_id: string }
export interface VarSet {
  name: string; level: VarLevel; scope?: string; environment?: string; type: VarType; of?: string; value: unknown; description?: string;
}

const q = (o: Record<string, string | undefined>) =>
  Object.entries(o).filter(([, v]) => v).map(([k, v]) => `${k}=${encodeURIComponent(v!)}`).join("&");

export const variablesApi = {
  at: (level: VarLevel, scope?: string, environment?: string, background = false) =>
    call<VarView>("GET", `/api/v1/variables?${q({ level, scope, environment })}`, undefined, { background }),
  project: (name: string, environment?: string, background = false) =>
    call<VarView>("GET", `/api/v1/projects/${encodeURIComponent(name)}/variables${environment ? "?environment=" + encodeURIComponent(environment) : ""}`, undefined, { background }),
  set: (v: VarSet) => call<{ changed: boolean }>("PUT", `/api/v1/variables/${encodeURIComponent(v.name)}`, v),
  remove: (name: string, level: VarLevel, scope?: string, environment?: string) =>
    call<unknown>("DELETE", `/api/v1/variables/${encodeURIComponent(name)}?${q({ level, scope, environment })}`),
  lock: (name: string, level: VarLevel, reason: string, scope?: string, environment?: string) =>
    call<{ overrides: unknown[] }>("PUT", `/api/v1/variables/${encodeURIComponent(name)}/lock`, { level, scope, environment, reason }),
  unlock: (name: string, level: VarLevel, scope?: string, environment?: string) =>
    call<unknown>("DELETE", `/api/v1/variables/${encodeURIComponent(name)}/lock?${q({ level, scope, environment })}`),
  run: (id: string) => call<{ run_id: string; rerun_of?: string; variables: RecordedVar[] }>("GET", `/api/v1/runs/${encodeURIComponent(id)}/variables`),
};

/** What to send for a value a person typed, by type. */
export function wireValue(type: VarType, text: string): unknown {
  switch (type) {
    case "number": return Number(text);
    case "boolean": return text === "true";
    case "list": return text.trim().startsWith("[") ? JSON.parse(text) : text.split(",").map((s) => s.trim()).filter(Boolean);
    case "map": return JSON.parse(text);
    case "secret": {
      const [store, rest = ""] = text.split(":", 2).length === 2 ? [text.slice(0, text.indexOf(":")), text.slice(text.indexOf(":") + 1)] : [text, ""];
      const [key, field] = rest.split("#");
      return { store, key, ...(field ? { field } : {}) };
    }
    default: return text;
  }
}
