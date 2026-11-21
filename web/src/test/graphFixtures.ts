import type { StepStatus, StepView } from "../api";
import { step } from "./fixtures";

/** Pipelines drawn as graphs in tests and screenshots. now is 1_000_000 ms past each step's start in these. */
const T0 = 1_700_000_000_000;
export const NOW = T0 + 600_000;

export function gs(id: string, needs: string[], status: StepStatus, over: Partial<StepView> = {}): StepView {
  const started = status === "pending" || status === "queued" || status === "skipped" ? 0 : T0 + 5_000;
  const finished = status === "succeeded" || status === "failed" ? T0 + 5_000 + 34_000 : 0;
  return step({ id, needs, status, started_at: started, finished_at: finished, ...over });
}

/** Launchway's own pipeline, mid-run: test, security-go and security-web run at the same time after check-code. */
export const OWN_PIPELINE: StepView[] = [
  gs("check-code", [], "succeeded"),
  gs("test", ["check-code"], "running"),
  gs("security-go", ["check-code"], "running"),
  gs("security-web", ["check-code"], "succeeded"),
  gs("build-ui", ["test"], "pending"),
  gs("image", ["build-ui", "security-go", "security-web"], "pending", { kind: "image" }),
  gs("deploy-dev", ["image"], "pending", { kind: "deploy", environment: "dev" }),
];

export const FAN_OUT: StepView[] = [
  gs("checkout", [], "succeeded"),
  ...Array.from({ length: 8 }, (_, i) => gs(`shard-${i + 1}`, ["checkout"], i < 3 ? "succeeded" : i < 6 ? "running" : "queued")),
  gs("merge-reports", Array.from({ length: 8 }, (_, i) => `shard-${i + 1}`), "pending"),
];

export const DIAMOND: StepView[] = [
  gs("build", [], "succeeded"), gs("unit", ["build"], "succeeded"), gs("lint", ["build"], "running"), gs("package", ["unit", "lint"], "pending"),
];

export const LONG_CHAIN: StepView[] = Array.from({ length: 9 }, (_, i) => gs(`stage-${i + 1}`, i ? [`stage-${i}`] : [], i < 4 ? "succeeded" : i === 4 ? "running" : "pending"));

export const FAILED_WITH_SKIPPED: StepView[] = [
  gs("check-code", [], "succeeded"),
  gs("test", ["check-code"], "failed", { exit_code: 2 }),
  gs("security-go", ["check-code"], "succeeded"),
  gs("security-web", ["check-code"], "succeeded"),
  gs("build-ui", ["test"], "skipped", { skip_kind: "not_needed" }),
  gs("image", ["build-ui", "security-go", "security-web"], "skipped", { skip_kind: "not_needed", kind: "image" }),
  gs("approve", ["image"], "skipped", { kind: "approval", skip_kind: "not_needed" }),
];

/** A step that waits for approval, a long skipping connector and a template group, for the odd cases. */
export const MIXED: StepView[] = [
  gs("check", [], "succeeded"),
  gs("build/compile", ["check"], "succeeded", { group: "build" }),
  gs("build/pack", ["build/compile"], "running", { group: "build" }),
  gs("docs", ["check"], "succeeded"),
  gs("approve", ["build/pack", "docs"], "waiting", { kind: "approval", message: "Release?" }),
  gs("ship", ["approve", "check"], "pending"),
];
export const MIXED_GROUPS = [{ id: "build", uses: "go/build", library: "go", template: "build", steps: ["build/compile", "build/pack"] }];
