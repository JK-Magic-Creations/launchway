import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { StepCause } from "../api";
import { explainFailure } from "../guided/derive";
import { step } from "../test/fixtures";
import { StepCauseView } from "./StepCause";

const cause: StepCause = {
  kind: "analysis_failed",
  summary: 'The dev deploy was stopped by its "smoke" analysis: smoke failed 1 of 20 measurements, and it allows 0 (could not resolve host).',
  analysis: {
    name: "shop-7-smoke", phase: "Failed", namespace: "shop",
    metrics: [{
      name: "smoke", phase: "Failed", count: 20, successful: 19, failed: 1, inconclusive: 0, error: 0, provider: "job", interval: "30s", failure_limit: 0,
      measurements: [{ phase: "Successful", at: 1 }, { phase: "Failed", message: "could not resolve host new.shop", at: 2 }],
      job: { name: "smoke-7", namespace: "shop" }, job_log: { note: "the job's log is in shop/smoke-7; this runner may not read it" },
    }],
  },
  next: { kind: "rolled_back", step: "rollback-dev", version: "v1.2.3", summary: "Rolled back to v1.2.3 by rollback-dev." },
};

describe("a failed step says why", () => {
  it("shows the sentence, the metric with its rule and failing measurement, the job, and what happened next", () => {
    render(<StepCauseView cause={cause} />);
    expect(screen.getByText(/stopped by its "smoke" analysis/)).toBeInTheDocument();
    expect(screen.getByText(/1 failed of 20 measured/)).toBeInTheDocument();
    expect(screen.getByText(/a Kubernetes job, every 30s; .*tolerates 0 failures/)).toBeInTheDocument();
    expect(screen.getByText("could not resolve host new.shop")).toBeInTheDocument();
    expect(screen.getByText("shop/smoke-7")).toBeInTheDocument();
    expect(screen.getByText(/this runner may not read it/)).toBeInTheDocument();
    expect(screen.getByText(/Rolled back to v1.2.3 by rollback-dev/)).toBeInTheDocument();
  });

  it("puts the server's sentence at the top of a failed run", () => {
    const f = explainFailure([step({ id: "deploy-dev", status: "failed", error: "the rollout was aborted", cause })])!;
    expect(f.kind).toBe("deploy");
    expect(f.headline).toBe(cause.summary);
    expect(f.cause?.analysis?.name).toBe("shop-7-smoke");
  });
});
