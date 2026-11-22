import { call } from "./api";

// The API of custom steps: what a step takes, gives and may do, from its manifest, and what a run
// recorded about one.

declare module "./api" {
  interface StepView {
    /** Set on a step that came from a custom step: who it is, from which library and commit, and what it may do. */
    custom?: CustomStepRecord;
    /** The summary and annotations a custom step wrote in its latest attempt. */
    notes?: StepNote[];
  }
}

export interface CustomStepRecord {
  name: string; library?: string; commit?: string; contract: number; digest: string;
  network?: string[]; write?: string[]; secrets?: string[];
  input_types?: Record<string, string>; output_types?: Record<string, string>;
}

export interface StepNote { kind: "summary" | "annotation"; level?: string; file?: string; line?: number; text: string }

export interface StepInput { name: string; type: string; required: boolean; default?: string; description?: string; options?: string[]; pattern?: string; min?: number; max?: number }
export interface StepOutput { name: string; type: string; description?: string }
export interface StepInfo {
  name: string; description?: string; contract: number; image: string; command: string; digest: string;
  inputs: StepInput[]; outputs: StepOutput[]; network: string[]; write: string[]; secrets: string[];
  permissions: { Kind: string; Detail: string }[]; sentence: string; cpu?: string; memory?: string; timeout?: string; example: string;
}
export interface StepChange { what: string; permission?: boolean; widening?: boolean }
export interface StepVersion { version: string; commit: string; digest: string; changes?: StepChange[]; widens?: boolean }
export interface StepUse {
  project: string; commit: string; digest: string; runs: number; last_run_at: number; last_run_id: string;
  newest?: string; changes?: StepChange[]; widens?: boolean;
}
export interface StepPageData {
  library: string; version: string; commit: string; step: StepInfo; versions: StepVersion[];
  newest?: string; newest_changes?: StepChange[]; newest_widens?: boolean; used_by: StepUse[];
}
export interface LibrarySteps { library: string; version: string; commit: string; versions: string[]; steps: (StepInfo & { problems?: string[] })[]; truncated?: boolean }

const enc = encodeURIComponent;

export const stepsApi = {
  list: (library: string, version?: string) => call<LibrarySteps>("GET", `/api/v1/libraries/${enc(library)}/steps${version ? "?version=" + enc(version) : ""}`),
  page: (library: string, step: string, version?: string) =>
    call<StepPageData>("GET", `/api/v1/libraries/${enc(library)}/steps/${enc(step)}${version ? "?version=" + enc(version) : ""}`),
};
