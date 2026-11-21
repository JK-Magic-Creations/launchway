import { call } from "./api";
import type { RollbackTarget, ServerAction } from "./apiDeploy";

// Rolling back after a deployment succeeded: what the server reports a person may roll back to, asking for it,
// and the review of emergency rollbacks. The server decides what is possible; the interface offers only that.

export interface RunRef { id: string; number: number; actor?: string; reason?: string }

export interface RollbackPlan {
  project: string; environment: string; service: string; protected: boolean;
  current?: { deployment: string; run: string; number: number; version: string };
  holder?: { deployment: string; run: string; number: number; version?: string };
  action: ServerAction;
}

export interface ReviewItem {
  run: string; number: number; project: string; environment: string; service: string;
  from_version: string; to_version: string; reason: string; actor: string; at: number;
  acknowledge: ServerAction;
}

/** What a rollback run is, for its page. */
export interface RollbackRunView {
  environment: string; service: string; headline: string;
  to: { deployment: string; version: string; run: string; run_number: number; at?: number };
  from: { deployment: string; version: string; run: string; run_number: number };
  reverts: { deployment: string; version: string; commit: string; run: string; run_number: number }[];
  reason: string; actor: string; emergency: boolean; review_required: boolean;
  review?: { by: string; at: number; comment: string };
  note: string;
}

/** What a deployment made by a rollback run restored. */
export interface DeploymentRollback {
  headline: string; to_version: string; from_version: string; actor: string; reason: string; emergency?: boolean;
  needs_review?: boolean; review?: { by: string; at: number; comment: string };
}

declare module "./api" {
  interface DeploymentView {
    rollback?: DeploymentRollback;
    rolled_back_by?: RunRef[];
    actions?: ServerAction[];
  }
  interface UnitView { rollback?: ServerAction }
  interface RunDetail {
    rollback?: RollbackRunView;
    rolled_back_by?: RunRef[];
  }
}

export interface RollbackRequest { environment: string; service?: string; to?: string; reason: string; queue?: boolean }

export const rollbackApi = {
  plan: (project: string, environment: string, service?: string) =>
    call<RollbackPlan>("GET", `/api/v1/projects/${encodeURIComponent(project)}/rollback?environment=${encodeURIComponent(environment)}${service ? `&service=${encodeURIComponent(service)}` : ""}`),
  request: (project: string, body: RollbackRequest) =>
    call<{ id: string; number: number; message: string }>("POST", `/api/v1/projects/${encodeURIComponent(project)}/rollbacks`, body),
  reviews: (background = false) => call<ReviewItem[]>("GET", "/api/v1/rollbacks/review", undefined, { background }),
  review: (run: string, comment: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${encodeURIComponent(run)}/review`, { comment }),
};

export type { RollbackTarget };
