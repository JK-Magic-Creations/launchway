import type { StepStatus } from "./api";

// Types of the deploy clarity work: analysis evidence, failure causes, server-computed actions.
export interface DeployMeasurement { phase: string; message?: string; value?: string; at?: number }

export interface DeployMetric {
  name: string; phase: string; message?: string;
  count: number; successful: number; failed: number; inconclusive: number; error: number;
  /** What the metric checks: provider (job, prometheus, web...), its conditions as written, how often, how many failures it allows. */
  provider?: string; success_condition?: string; failure_condition?: string; interval?: string; failure_limit?: number;
  measurements?: DeployMeasurement[];
  /** The Kubernetes Job a job metric ran, and the end of its log when it failed (or why there is none). */
  job?: { name: string; namespace: string };
  job_log?: { lines?: string[]; note?: string };
}

/** One of a rollout's own analysis runs. */
export interface DeployAnalysis {
  name: string;
  phase: string; // Pending, Running, Successful, Failed, Error, Inconclusive
  message?: string;
  kind?: string; // Step or Background
  step_index?: number;
  pod_hash?: string;
  revision?: string;
  created_at?: number;
  namespace?: string;
  metrics?: DeployMetric[];
}

/** An action as the server computes it: the interface only ever offers what is available, and says why not otherwise. */
export interface ServerAction {
  name: string; available: boolean; reason?: string; instead?: string; would_help?: string; who: string;
  /** Risky but allowed: show the consequence and the suggestion, then confirm. */
  risk?: string; suggestion?: string;
  /** A rollback: the earlier successful deployments it can restore, the previous one first. */
  targets?: RollbackTarget[];
  /** Not available only because another deployment holds the lock: asking to queue behind it works. */
  queueable?: boolean;
  /** An emergency action on a protected environment: no approval first, an approver reviews it afterwards. */
  emergency?: boolean;
  /** A setting asks for an approval first, even for an emergency. */
  requires_approval?: boolean;
}

/** An earlier successful deployment a service can be rolled back to, and what choosing it does. */
export interface RollbackTarget {
  deployment: string; run: string; run_number: number; version: string;
  /** When it was deployed (Unix ms). */
  at: number;
  /** The previous successful deployment: the default. */
  previous?: boolean;
  /** The deployments that stop running, newest first. */
  reverts: { deployment: string; run: string; run_number: number; version: string; at: number }[];
  consequence: string;
  suggestions?: string[];
}

/** Why a failed step failed: the kind, one plain sentence, the evidence, and what Launchway did next. */
export interface StepCause {
  kind: "analysis_failed" | "rollout_failed" | "sync_failed" | "timeout" | "script_exit" | "refused" | "runner_lost" | "command_failed" | "other";
  summary: string;
  environment?: string;
  phase?: string;
  analysis?: DeployAnalysis;
  exit_code?: number;
  next?: { kind: "rolled_back" | "rolling_back" | "rollback_failed" | "no_rollback"; step?: string; version?: string; summary: string };
}


export interface RerunPlan {
  run: string[]; reuse: string[];
  /** How each reused step ended (an approval: who decided it; an image step: its outputs, with the digest). */
  reused?: { id: string; kind?: string; status: StepStatus; decided_by?: string; decided_at?: number; outputs?: Record<string, string>; approvers?: string[] }[];
  /** Approvals among the steps that run again: they ask again. */
  approvals_again?: string[];
  run_number?: number;
  /** A deploy step runs again: a new deployment of the same version is made. */
  deployment?: boolean;
}

