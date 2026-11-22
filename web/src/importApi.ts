// The importer's call (POST /api/v1/import/pipeline) and its answer. It mirrors internal/api/routes_import.go.
import { call } from "./api";

export type FindingClass = "exact" | "different" | "manual";
export interface Finding { code: string; class: FindingClass; message: string; next: string; file: string; line: number; step?: string; fix?: string; name?: string }
export interface StepRef { job: string; step: string; line: number; template?: string }
export interface Plan { format: string; files: string[]; name: string; pipeline: string; findings: Finding[]; steps: StepRef[]; secrets: string[]; dry: boolean }
export interface Detected { path: string; format: string }
export interface ImportResult { plans: Plan[]; detected: Detected[]; message?: string }
export interface ImportRequest {
  format?: string; files: { path: string; content: string }[]; root?: string; dry?: boolean; name?: string;
  secret_store?: string; default_image?: string; no_templates?: boolean; templates?: Record<string, string>; secret_keys?: Record<string, string>;
}

/** The formats the importer reads, with a path to suggest for pasted text. The ids are the server's. */
// The picker names the file a person already owns (see factualFileNames in internal/archtest/originality_test.go).
export const FORMATS: { id: string; title: string; path: string }[] = [
  { id: "hosted-workflows", title: "Hosted workflow file", path: ".github/workflows/ci.yml" },
  { id: "stages-yaml", title: "Stage-based pipeline file (.gitlab-ci.yml)", path: ".gitlab-ci.yml" },
  { id: "groovy-declarative", title: "Declarative Groovy pipeline file (Jenkinsfile)", path: "Jenkinsfile" },
  { id: "jobs-config", title: "Jobs-and-workflows config file (.circleci/config.yml)", path: ".circleci/config.yml" },
  { id: "tasks-pipelines", title: "Tasks-and-stages pipeline file (bitbucket-pipelines.yml)", path: "bitbucket-pipelines.yml" },
];

export const importApi = {
  run: (req: ImportRequest) => call<ImportResult>("POST", "/api/v1/import/pipeline", req),
};

/** Where in a repository to look for pipeline files of other systems: the directories of well-known layouts and every top-level file or dot-directory. */
export const LOOK_IN = [".github/workflows", ".ci"];
