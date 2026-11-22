// Calls for connections to outside Git hosts and the Flights kept on them. They mirror internal/api/outside_hosts.go.

import { call } from "./api";

export interface Declaration { support: "native" | "emulated" | "none"; reason?: string; workaround?: string }

export interface Addition { key: string; title: string; what: string; drivable: boolean }

export interface Connection {
  additions?: Addition[];
  name: string; kind: string; base_url?: string; upload_url?: string;
  /** Where the token is read from ("env:NAME"); never the token. */
  credential: string; webhook_secret?: string; auth_kind: string; owners?: string[]; enabled: boolean;
  capabilities?: Record<string, Declaration>;
}

export interface NewConnection {
  name: string; kind: string; base_url?: string; owners?: string[];
  credential: { env?: string; file?: string }; webhook_secret?: { env?: string; file?: string };
}

export interface PermissionCheck { feature: string; needs: string; ok: boolean; detail?: string }

export interface ConnectionTest {
  connection: string;
  report: { account: string; token_expires?: string; rate_remaining: number; checks: PermissionCheck[]; repositories: number };
}

export interface OutsideRepository { id: string; path: string; name: string; default_branch?: string; visibility: string; clone: { https?: string; ssh?: string } }

export interface RepositoryPage { items: OutsideRepository[]; next?: string }

export interface FlightOutside {
  project: string; connection: string; repository: string; kind?: string;
  report_statuses: boolean; report_comment: boolean;
  /** What the host cannot do, by feature, in the host's words. */
  unsupported?: Record<string, Declaration>;
  webhook_url?: string; last_delivery_at?: number;
  /** What the host offers beyond the built-in forge. */
  additions?: Addition[];
}

const enc = encodeURIComponent;

/** What each host kind can do, from the server's own declarations. */
export interface HostMatrix {
  features: { key: string; title: string }[];
  kinds: { kind: string; label: string; capabilities: Record<string, Declaration> }[];
}

export const outsideApi = {
  matrix: () => call<HostMatrix>("GET", "/api/v1/outside-host-matrix"),
  connections: () => call<{ items: Connection[] }>("GET", "/api/v1/outside-hosts").then((p) => p.items ?? []),
  add: (c: NewConnection) => call<Connection>("POST", "/api/v1/outside-hosts", c),
  remove: (name: string) => call<Connection>("DELETE", `/api/v1/outside-hosts/${enc(name)}`),
  test: (name: string) => call<ConnectionTest>("POST", `/api/v1/outside-hosts/${enc(name)}/test`, {}),
  repositories: (name: string, q: string, cursor?: string) =>
    call<RepositoryPage>("GET", `/api/v1/outside-hosts/${enc(name)}/repositories?q=${enc(q)}${cursor ? `&cursor=${enc(cursor)}` : ""}`),
  flight: (project: string) => call<FlightOutside>("GET", `/api/v1/projects/${enc(project)}/outside`),
  attach: (project: string, body: { connection?: string; repository?: string; report_statuses?: boolean; report_comment?: boolean }) =>
    call<FlightOutside>("PUT", `/api/v1/projects/${enc(project)}/outside`, body),
  detach: (project: string) => call<void>("DELETE", `/api/v1/projects/${enc(project)}/outside`),
};

/** The words for a feature the host lacks, as a sentence per feature. */
export const FEATURE_WORDS: Record<string, string> = {
  "releases.tags": "make a release tag", "releases.publish": "publish a release with files", "content.write": "commit a file or open a pull request for a Launch write-back",
  "proposals.summary_comment": "post the one summary comment on a pull request", "results": "show a status for a Sortie", "land": "merge a pull request",
  "reviews": "submit a review", "rules": "change branch protection", "repositories.create": "create a repository", "proposals": "work with pull requests",
};
