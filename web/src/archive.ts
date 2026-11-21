// Types and calls for the archive of another server's issues. They mirror
// internal/forge/archive and internal/api/forge_archive.go.

import { call } from "./api";

export interface ArchivedComment { id?: string; author: string; body: string; created_at?: string }

export interface ArchivedIssue {
  number: number;
  title: string;
  state: "open" | "closed";
  author: string;
  body: string;
  labels: string[];
  milestone?: string;
  assignees: string[];
  url?: string;
  is_pull_request?: boolean;
  created_at?: string;
  closed_at?: string;
  comment_count: number;
  comments?: ArchivedComment[];
}

export interface ArchivePage { items: ArchivedIssue[]; total: number; open: number; closed: number; page: number; per_page: number }

const base = (path: string) => "/api/v1/repositories/" + path.split("/").map(encodeURIComponent).join("/") + "/archive/issues";

export const archiveApi = {
  list: (path: string, q: { state: string; text: string; page: number }) => {
    const p = new URLSearchParams({ page: String(q.page) });
    if (q.state && q.state !== "all") p.set("state", q.state);
    if (q.text.trim()) p.set("q", q.text.trim());
    return call<ArchivePage>("GET", base(path) + "?" + p.toString());
  },
  get: (path: string, number: number) => call<ArchivedIssue>("GET", `${base(path)}/${number}`),
};
