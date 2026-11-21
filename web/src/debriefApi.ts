import { call } from "./api";

// The API of the Debrief: the report of a run's tests, a project's flaky
// tests, and the exports. Loaded only by the pages that show them.

export interface DebriefRunRef { id: string; number: number; ref: string; branch: string; sha: string; created_at: number; finished_at?: number; actor?: string; link?: string }
export interface DebriefTestName { key: string; suite: string; name: string }
export interface DebriefSummary { total: number; passed: number; failed: number; errored: number; skipped: number; flaky: number; quarantined: number; duration_ms: number; pass_rate: number; truncated?: boolean }
export interface DebriefSlower extends DebriefTestName { base_ms: number; ms: number }
export interface DebriefChange { base: DebriefRunRef; newly_failing: number; newly_passing: number; total_delta: number; duration_delta_ms: number; slower: DebriefSlower[]; newly_passing_tests?: DebriefTestName[] }
export interface DebriefAttachment { name: string; kind: string; content_type?: string; bytes?: number; link?: string }
export interface DebriefDiffLine { op: " " | "-" | "+"; text: string }
export interface DebriefDiff { expected: string; actual: string; lines?: DebriefDiffLine[] }
export interface DebriefFailure extends DebriefTestName {
  case_id?: number; file?: string; line?: number; variant?: string; step?: string; status: "failed" | "errored"; duration_ms: number;
  message?: string; details?: string; output?: string; diff?: DebriefDiff; owner?: string; new_here?: boolean;
  first_failed_in?: DebriefRunRef; failing_on_base?: "yes" | "no" | "unknown" | ""; attachments?: DebriefAttachment[]; retried?: boolean; link?: string; flaky_score?: number;
}
export interface DebriefFlaky extends DebriefTestName { score: number; reason: string; this_run: string; branches?: string[]; first_seen?: number; commits: number; same_commit: number }
export interface DebriefQuarantined extends DebriefTestName { owner?: string; expires?: string; reason?: string; message?: string; status: string; expired?: boolean }
export interface DebriefSlow extends DebriefTestName { duration_ms: number; avg_ms?: number }
export interface DebriefSuite { name: string; depth: number; total: number; passed: number; failed: number; skipped: number; duration_ms: number }
export interface DebriefMatrix { columns: string[]; rows: (DebriefTestName & { cells: string[] })[]; cut?: boolean }
export interface DebriefRatio { covered: number; total: number; percent: number }
export interface DebriefFileCoverage { path: string; covered: number; total: number; percent: number; missed?: string }
export interface DebriefCoverage {
  lines: DebriefRatio; branches: DebriefRatio; functions: DebriefRatio; base_lines?: number; floor?: number;
  change?: { covered: number; total: number; percent: number; files: DebriefFileCoverage[]; advisory?: string; base_ref?: string };
  least: DebriefFileCoverage[]; files: number;
}
export interface DebriefTrendPoint { number: number; sha: string; at: number; total: number; pass_rate: number; duration_ms: number; flaky: number }
export interface Debrief {
  version: number; generated_at: number; install: { name: string }; project: string; run: DebriefRunRef; result: string; summary: DebriefSummary;
  change?: DebriefChange; failures: DebriefFailure[]; flaky: DebriefFlaky[]; quarantined: DebriefQuarantined[]; slowest: DebriefSlow[];
  suites: DebriefSuite[]; matrix?: DebriefMatrix; coverage?: DebriefCoverage; trend: DebriefTrendPoint[]; notes: string[];
}

export interface FlakyTest {
  key: string; suite: string; name: string; file?: string; variant?: string; score: number; reason: string; commits: number; same_commit: number; blips: number;
  branches: string[]; first_seen: number; last_run: number; last_status: string; history: string[]; suggest: string;
}
export interface FlakyList { project: string; window: number; tests: FlakyTest[]; rules: string; considered: number }

export const debriefApi = {
  debrief: (run: string, background = false) => call<Debrief>("GET", `/api/v1/runs/${run}/debrief`, undefined, { background }),
  flaky: (project: string) => call<FlakyList>("GET", `/api/v1/projects/${encodeURIComponent(project)}/tests/flaky`),
};

export const EXPORT_FORMATS: { format: string; label: string; what: string }[] = [
  { format: "html", label: "HTML", what: "One file that opens offline, light or dark" },
  { format: "pdf", label: "PDF", what: "A printable report with contents and page numbers" },
  { format: "json", label: "JSON", what: "The same facts for other tools" },
  { format: "junit", label: "JUnit XML", what: "Every result, merged, for tools that read JUnit" },
];

export function exportHref(run: string, format: string): string {
  return `/api/v1/runs/${run}/debrief/export?format=${format}`;
}
