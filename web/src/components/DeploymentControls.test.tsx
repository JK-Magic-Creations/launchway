import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { DeploymentView, RunDetail } from "../api";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, deploymentControls: vi.fn(), command: vi.fn().mockResolvedValue({ id: "c9", state: "pending", action: "promote-full", actor: "ann", origin: "person", expect: {}, tries: 0, at: 1 }) } };
});

import { api } from "../api";
import { DeploymentControls } from "./DeploymentControls";

const why = "this deploy runner may not change the status of rollout shop/shop; an administrator must list the rollout in the chart value delivery.deployRunner.rbac.controlRollouts";
const dep = {
  id: "d1", run_id: "run_1", environment: "dev", service: "shop", phase: "rollout_paused", detail: "", observed_at: Date.now(),
  snapshot: { rollout: { found: true, phase: "Paused", paused: true, aborted: false, pause_reasons: ["CanaryPauseStep"], steps: 3, generation: 1, observed: true, replicas: 2, ready: 2, updated: 1, available: 2 } },
  commands: [{ id: "c1", deployment: "d1", run_id: "run_1", step: "deploy-dev", action: "promote-full", actor: "ann", origin: "person", expect: { pod_hash: "h2" }, state: "done", result: "failed", detail: why, tries: 1, at: 1, done_at: 2 }],
} as unknown as DeploymentView;

// The rule: a button is enabled only for an action the server reports available; the others are disabled with the reason, and a failed command says so.
describe("rollout controls follow what the server reports", () => {
  it("shows every action the runner may not apply as disabled, with the reason", async () => {
    vi.mocked(api.deploymentControls).mockResolvedValue({ environment: "dev", protected: false, withdraw: true, withdraw_reason: "",
      controls: ["promote", "promote-full", "abort", "retry"].map((action) => ({ action, available: false, runner_cannot: true, reason: why })) } as never);
    render(<DeploymentControls run={{ id: "run_1" } as RunDetail} d={dep} now={Date.now()} />);
    expect(await screen.findByRole("button", { name: "Promote full" })).toBeDisabled();
    for (const b of screen.getAllByRole("button")) expect(b).toBeDisabled();
    expect(screen.getAllByText(/rbac\.controlRollouts/).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("alert").some((a) => /did not take effect/.test(a.textContent ?? ""))).toBe(true);
  });
  it("enables exactly the actions the server reports available", async () => {
    vi.mocked(api.deploymentControls).mockResolvedValue({ environment: "dev", protected: false, withdraw: true, withdraw_reason: "",
      controls: [{ action: "promote", available: true }, { action: "promote-full", available: true }, { action: "abort", available: false, reason: "the rollout is over" }, { action: "retry", available: false, reason: "the rollout is not aborted" }] } as never);
    render(<DeploymentControls run={{ id: "run_1" } as RunDetail} d={{ ...dep, commands: [] }} now={Date.now()} />);
    expect(await screen.findByRole("button", { name: "Promote" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Promote full" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("asks for confirmation, with the consequence and a suggestion, before a risky action, and then does it", async () => {
    vi.mocked(api.deploymentControls).mockResolvedValue({ environment: "dev", protected: false, withdraw: true, withdraw_reason: "",
      controls: [{ action: "promote", available: true }, { action: "promote-full", available: true }, { action: "abort", available: true }, { action: "retry", available: false }] } as never);
    const d = { ...dep, commands: [], snapshot: { ...dep.snapshot, analysis: [{ name: "a", phase: "Failed" }] } } as unknown as DeploymentView;
    render(<DeploymentControls run={{ id: "run_1" } as RunDetail} d={d} now={Date.now()} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Promote full" }));
    expect(api.command).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog", { name: "Confirm full promotion" }).textContent).toMatch(/failed.*Suggestion/);
    // Promoting fully needs a reason, which the server also asks for.
    expect(screen.getByRole("button", { name: "Yes, promote fully" })).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "Comment on the command" }), "the analysis is flaky");
    await user.click(screen.getByRole("button", { name: "Yes, promote fully" }));
    expect(api.command).toHaveBeenCalledWith("run_1", "d1", "promote-full", "the analysis is flaky");
  });
});
