import { call } from "./api";

// The API of the built-in registry: what a project has pushed, and its policy.

export interface RegistryStatus {
  enabled: boolean;
  address: string;
  /** What clients write before a repository name. */
  host: string;
  insecure: boolean;
  store: string;
  login: string;
  repositories: number;
  encryption: { blobs_encrypted_by_launchway: boolean; note: string };
  limits: { max_blob_bytes: number; max_manifest_bytes: number; default_quota_bytes: number; requests_per_minute: number; gc_grace_seconds: number };
}

export interface RegistryRetention { keep_last?: number; keep_tags?: string[]; untagged_days?: number }

export interface RegistryPolicy {
  quota_bytes?: number;
  retention?: RegistryRetention;
  immutable_tags?: string[];
  pull_from?: string[];
}

export interface RegistryEffective {
  quota_bytes: number;
  quota_from: string;
  retention: RegistryRetention;
  retention_from?: string;
  has_retention: boolean;
  immutable_tags?: string[];
  pull_from?: string[];
}

export interface RegistryRepo { name: string; images: number; tags: number; bytes: number; pushed_at: number }

export interface ProjectRegistry {
  project: string;
  host: string;
  insecure: boolean;
  repositories: RegistryRepo[];
  used_bytes: number;
  /** -1: no quota. */
  quota_bytes: number;
  quota_from: string;
  policy: RegistryEffective;
  push_example: string;
}

export interface RegistryAttachment { digest: string; artifact_type?: string; media_type: string; tags?: string[] }

export interface RegistryImage {
  digest: string;
  media_type: string;
  artifact_type?: string;
  tags: string[];
  total_size: number;
  platforms?: string[];
  created_at: number;
  created_by?: string;
  run_id?: string;
  signed: boolean;
  has_sbom: boolean;
  has_provenance: boolean;
  vulnerabilities?: { critical: number; high: number; medium: number; low: number; scanner?: string };
  attached?: RegistryAttachment[];
  children?: string[];
  pull: string;
}

export interface RetentionCandidate { repository: string; digest: string; tags: string[]; reason: string; size: number; created_at: number; attached: number }

const base = (project: string) => `/api/v1/projects/${encodeURIComponent(project)}/registry`;
const q = (repository: string) => `?repository=${encodeURIComponent(repository)}`;

export const registryApi = {
  status: () => call<RegistryStatus>("GET", "/api/v1/registry"),
  project: (project: string, background = false) => call<ProjectRegistry>("GET", base(project), undefined, { background }),
  images: (project: string, repository: string, background = false) =>
    call<{ repository: string; images: RegistryImage[] }>("GET", `${base(project)}/images${q(repository)}`, undefined, { background }),
  inspect: (project: string, repository: string, reference: string) =>
    call<{ image: RegistryImage; manifest: string }>("GET", `${base(project)}/image${q(repository)}&reference=${encodeURIComponent(reference)}`),
  deleteImage: (project: string, repository: string, digest: string) =>
    call<{ deleted: string[] }>("DELETE", `${base(project)}/image${q(repository)}&reference=${encodeURIComponent(digest)}`),
  deleteTag: (project: string, repository: string, tag: string) =>
    call<{ deleted: boolean }>("DELETE", `${base(project)}/tag${q(repository)}&tag=${encodeURIComponent(tag)}`),
  setPolicy: (project: string, policy: RegistryPolicy) => call<{ policy: RegistryPolicy }>("PUT", `${base(project)}/policy`, policy),
  policy: (project: string) => call<{ set: boolean; policy: RegistryPolicy; effective: RegistryEffective }>("GET", `${base(project)}/policy`),
  retentionPreview: (project: string) => call<{ candidates: RetentionCandidate[] }>("GET", `${base(project)}/retention`),
  retentionRun: (project: string) => call<{ deleted: RetentionCandidate[] }>("POST", `${base(project)}/retention`, {}),
};
