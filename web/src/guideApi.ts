// Types and calls of the first-run guide: internal/api/guide_*.go. Each step comes with its actions: what this
// person can do now (available) and, when not, why; a button is drawn only for an available action.

import { call } from "./api";

export interface GuideAction { name: string; available: boolean; reason?: string; would_help?: string; who: string }
export interface GuideStep { id: string; title: string; state: "done" | "todo" | "unavailable"; summary: string; actions: GuideAction[] }
export interface ChecklistItem { id: string; state: "ok" | "attention" | "unknown"; title: string; detail: string }
export interface SetupGuide { steps: GuideStep[]; checklist: ChecklistItem[]; kubernetes: boolean }

export interface SigninTestBegin { url: string; redirect_url: string; expires_in_seconds: number }
export interface SigninTestResult { tested: boolean; ok?: boolean; at?: number; detail?: string }
export interface RootKeyCopyStatus { taken: boolean; taken_at?: number; can_take: boolean; reason?: string; next?: string }

export const guideApi = {
  get: () => call<SetupGuide>("GET", "/api/v1/setup-guide"),
  testSignin: (b: { issuer: string; client_id: string; client_secret_env: string; groups_claim?: string; username_claim?: string }) =>
    call<SigninTestBegin>("POST", "/api/v1/signin/test", b),
  signinResult: () => call<SigninTestResult>("GET", "/api/v1/signin/test", undefined, { background: true }),
  keyCopyStatus: () => call<RootKeyCopyStatus>("GET", "/api/v1/encryption/root-key-copy"),
  takeKeyCopy: (b: { current_password: string; code: string; passphrase: string; passphrase_again: string }) =>
    call<Record<string, unknown>>("POST", "/api/v1/encryption/root-key-copy", b),
  addConnectionWithToken: (b: { name: string; kind: string; base_url: string; token: string }) =>
    call<{ name: string; kind: string; base_url?: string }>("POST", "/api/v1/connections/token", b),
};

/** The action of a step by name, or undefined. */
export const actionOf = (s: GuideStep | undefined, name: string) => s?.actions.find((a) => a.name === name);

// What a person skipped is kept in this browser: a convenience for coming back to where they were. The server's
// facts (runners connected, connections made) decide what is done; nothing here is needed for the guide to work.
const KEY = "launchway.guide.skipped";
export function loadSkipped(): string[] {
  try { const v = JSON.parse(localStorage.getItem(KEY) ?? "[]"); return Array.isArray(v) ? v.filter((x) => typeof x === "string") : []; } catch { return []; }
}
export function saveSkipped(ids: string[]) {
  try { localStorage.setItem(KEY, JSON.stringify(ids)); } catch { /* private window or blocked storage: the guide still works */ }
}
