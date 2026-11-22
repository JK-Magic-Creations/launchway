// Calls for reading and changing a hosted repository's code. They mirror internal/api/forge_files.go and
// forge_browse.go. Everything a repository holds was written by others: it is shown as text, never as markup.

import { call } from "./api";
import type { BranchComparison, FileChange, Patch } from "./pullsApi";

export interface LastCommit { sha: string; subject: string; author: string; at: string }
export interface Entry { name: string; path: string; type: "file" | "dir" | "symlink" | "submodule"; size?: number; sha: string; last?: LastCommit }
export interface Item {
  path: string; type: "dir" | "file" | "symlink" | "submodule"; ref: string; commit: string; sha: string; size?: number; mode?: string;
  encoding?: "utf8" | "base64"; content?: string; truncated?: boolean; entries?: Entry[]; last_bounded?: boolean;
}
export interface CommitEntry {
  sha: string; subject: string; message: string; author: string; authored_at: string; committer: string; committed_at: string; parents: number; parent_shas: string[];
}
export interface RefInfo {
  name: string; kind: "branch" | "tag"; sha: string; commit: string; subject: string; author: string; committed_at: string; annotated?: boolean; message?: string; tagger?: string;
  ahead: number; behind: number; default?: boolean; protected?: boolean; open_pull?: number; base?: string;
}
export interface RunBrief { id: string; project: string; number: number; pipeline: string; status: string; created_at: number }
export interface BlameLine { n: number; sha: string; text: string; origin: number }
export interface BlameCommit { sha: string; author: string; at: string; subject: string; previous?: string; previous_path?: string; path: string }
export interface Blame { path: string; commit: string; total: number; from: number; to: number; next?: number; lines: BlameLine[]; commits: Record<string, BlameCommit> }
export interface Match { line: number; text: string; ranges?: [number, number][] }
export interface FileMatches { path: string; path_match?: boolean; matches: Match[] }
export interface SearchResult { commit: string; files: FileMatches[]; matches: number; partial: boolean; reason?: string; millis: number }
export interface SearchQuery { q: string; ref?: string; regex?: boolean; matchCase?: boolean; in?: string; dir?: string; ext?: string }
export interface Written { branch: string; path: string; commit: string; sha: string; parent: string }

export interface InspectionState {
  available: boolean; reason?: string; project?: string; branch?: string;
  runs: { id: string; number: number; status: string; sha: string; created_at: number }[];
}

const enc = encodeURIComponent;
const base = (path: string) => `/api/v1/repositories/${path.split("/").map(enc).join("/")}`;
export const qs = (o: Record<string, string | number | boolean | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== "" && v !== false) p.set(k, String(v === true ? 1 : v));
  const s = p.toString();
  return s ? "?" + s : "";
};

/** archiveUrl is where a snapshot of a ref is: a tar.gz or a zip of its files, streamed by the server. */
export const archiveUrl = (repo: string, ref: string, format: "tar.gz" | "zip") => `/api/v1/repositories/${repo.split("/").map(encodeURIComponent).join("/")}/download?ref=${encodeURIComponent(ref)}&format=${format}`;

/** rawUrl is where a file's bytes are: a download, or with inline a picture shown in the page. */
export const rawUrl = (path: string, ref: string, file: string, inline = false) => `${base(path)}/raw${qs({ ref, path: file, inline })}`;

/** Where a place in the code lives in the product's addresses. The ref and the path are in the query, so a branch
 * name with slashes is never ambiguous; a line range is the fragment (#L10-L20). */
export const codeHref = (repo: string, ref?: string, p?: string, extra: Record<string, string> = {}) =>
  `/repositories/${repo}/code${qs({ ref, path: p, ...extra })}`;
export const blameHref = (repo: string, ref: string, p: string) => `/repositories/${repo}/blame${qs({ ref, path: p })}`;
export const commitsHref = (repo: string, ref?: string, p?: string) => `/repositories/${repo}/commits${qs({ ref, path: p })}`;
export const commitHref = (repo: string, sha: string) => `/repositories/${repo}/commit/${sha}`;
export const compareHref = (repo: string, head: string, baseRef?: string) => `/repositories/${repo}/compare${qs({ head, base: baseRef })}`;
export const searchHref = (repo: string, q?: string, ref?: string) => `/repositories/${repo}/search${qs({ q, ref })}`;

export const browseApi = {
  read: (repo: string, ref: string | undefined, p: string, last = false) => call<Item>("GET", `${base(repo)}/files${qs({ ref, path: p, last })}`),
  commits: (repo: string, ref: string | undefined, p: string | undefined, limit = 50) => call<{ items: CommitEntry[]; ref: string }>("GET", `${base(repo)}/commits${qs({ ref, path: p, limit })}`),
  commit: (repo: string, sha: string) => call<{ commit: CommitEntry; runs: RunBrief[] }>("GET", `${base(repo)}/commits/${enc(sha)}`),
  diff: (repo: string, from: string, to: string) => call<{ files: FileChange[]; total: number; additions: number; deletions: number; truncated: boolean }>("GET", `${base(repo)}/diff${qs({ from, to, limit: 500 })}`),
  patch: (repo: string, from: string, to: string, file: string, full: boolean) => call<Patch>("GET", `${base(repo)}/diff/patch${qs({ from, to, path: file, full })}`),
  branches: (repo: string, q = "", limit = 100) => call<{ items: RefInfo[]; total: number; truncated: boolean; default_branch: string }>("GET", `${base(repo)}/branches${qs({ q, limit })}`),
  tags: (repo: string, q = "", limit = 100) => call<{ items: RefInfo[]; total: number; truncated: boolean }>("GET", `${base(repo)}/tags${qs({ q, limit })}`),
  makeBranch: (repo: string, name: string, from: string) => call<{ name: string; commit: string }>("POST", `${base(repo)}/branches`, { name, from }),
  deleteBranch: (repo: string, name: string) => call<{ name: string; was: string; undo: string }>("DELETE", `${base(repo)}/branches${qs({ name })}`),
  renameBranch: (repo: string, name: string, newName: string) => call<{ name: string; was: string; commit: string; note: string }>("POST", `${base(repo)}/branches/rename`, { name, new_name: newName }),
  makeTag: (repo: string, name: string, from: string) => call<{ name: string; commit: string }>("POST", `${base(repo)}/tags`, { name, from }),
  deleteTag: (repo: string, name: string) => call<{ name: string; was: string; undo: string }>("DELETE", `${base(repo)}/tags${qs({ name })}`),
  upload: (repo: string, b: { branch: string; message: string; replace: boolean; files: { path: string; content: string; encoding: string }[] }) =>
    call<{ branch: string; paths: string[]; commit: string; parent: string }>("POST", `${base(repo)}/files`, b),
  inspections: (repo: string) => call<InspectionState>("GET", `${base(repo)}/inspections`),
  inspect: (repo: string, ref?: string) => call<{ run: { id: string; number: number }; project: string }>("POST", `${base(repo)}/inspections`, { ref: ref ?? "" }),
  blame: (repo: string, ref: string, p: string, from?: number, to?: number) => call<Blame>("GET", `${base(repo)}/blame${qs({ ref, path: p, from, to })}`),
  search: (repo: string, q: SearchQuery) => call<SearchResult>("GET", `${base(repo)}/search${qs({ q: q.q, ref: q.ref, regex: q.regex, case: q.matchCase, in: q.in, dir: q.dir, ext: q.ext })}`),
  compare: (repo: string, head: string, target?: string) => call<BranchComparison>("GET", `${base(repo)}/compare${qs({ head, base: target })}`),
  write: (repo: string, b: { branch: string; path: string; content: string; message: string; expected_sha?: string }) => call<Written>("PUT", `${base(repo)}/files`, b),
  remove: (repo: string, b: { branch: string; path: string; message: string; expected_sha: string }) => call<Written>("DELETE", `${base(repo)}/files`, b),
  protected: (repo: string) => call<{ patterns: string[] }>("GET", `${base(repo)}/protected-branches`).then((r) => r.patterns),
};

/** A branch name as the rules of protected branches see it: a pattern with * matches. */
export function isProtected(patterns: string[], branch: string): boolean {
  return patterns.some((p) => new RegExp("^" + p.split("*").map((x) => x.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$").test(branch));
}
