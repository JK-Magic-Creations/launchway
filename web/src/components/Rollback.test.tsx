import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RunDetail } from "../api";
import type { ServerAction } from "../apiDeploy";

vi.mock("../apiRollback", async (orig) => {
  const real = await orig<typeof import("../apiRollback")>();
  return { ...real, rollbackApi: { ...real.rollbackApi, request: vi.fn().mockResolvedValue({ id: "run_rb", number: 9, message: "m" }), review: vi.fn().mockResolvedValue({ ok: true }), reviews: vi.fn().mockResolvedValue([]) } };
});
vi.mock("../router", async (orig) => ({ ...(await orig<typeof import("../router")>()), navigate: vi.fn() }));

import { rollbackApi, type ReviewItem } from "../apiRollback";
import { navigate } from "../router";
import { RollbackControl } from "./Rollback";
import { NeedsReview, RolledBackLine } from "./RollbackRun";

const unit = { project: "shop", service: "shop", environment: "dev" };
const target = (version: string, n: number, previous = false) => ({
  deployment: "dep_" + version, run: "run_" + n, run_number: n, version, at: 1_700_000_000_000, previous,
  reverts: [{ deployment: "dep_x", run: "run_9", run_number: 9, version: "1.4.0", at: 1 }],
  consequence: `dev goes from 1.4.0 back to ${version}; the deployment of 1.4.0 (run #9) is reverted in Git; the database and configuration are not rolled back`,
  suggestions: ["If 1.4.0 included a database migration, check that it is backward compatible with " + version + ": a rollback never undoes a database."],
});
const available: ServerAction = { name: "rollback", available: true, who: "operator", targets: [target("1.3.2", 7, true), target("1.3.0", 5)] };

beforeEach(() => { vi.clearAllMocks(); });

// The rule: a button is shown only where the server says it will work; otherwise the reason is shown, not a button that fails.
describe("the rollback control follows what the server reports", () => {
  it("offers the previous version and a picker, and confirms with the consequence, suggestions and a required reason", async () => {
    render(<RollbackControl action={available} unit={unit} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Roll back to 1\.3\.2/ }));
    const dlg = screen.getByRole("dialog", { name: "Roll back shop in dev to 1.3.2" });
    expect(dlg.textContent).toMatch(/dev goes from 1\.4\.0 back to 1\.3\.2/);
    expect(dlg.textContent).toMatch(/never restores a database or configuration/);
    expect(dlg.textContent).toMatch(/database migration/);
    const go = within(dlg).getByRole("button", { name: "Roll back to 1.3.2" });
    expect(go).toBeDisabled(); // the reason comes first
    await user.type(screen.getByRole("textbox"), "checkout fails");
    expect(go).toBeEnabled();
    await user.click(go);
    expect(rollbackApi.request).toHaveBeenCalledWith("shop", { environment: "dev", service: undefined, to: "dep_1.3.2", reason: "checkout fails", queue: false });
    expect(navigate).toHaveBeenCalledWith("/runs/run_rb");
  });

  it("lets an older deployment be chosen, with what choosing it reverts", async () => {
    render(<RollbackControl action={available} unit={unit} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Roll back to…" }));
    await user.click(screen.getByRole("radio", { name: /1\.3\.0/ }));
    expect(screen.getByRole("dialog").textContent).toMatch(/back to 1\.3\.0/);
    await user.type(screen.getByRole("textbox"), "older is better");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Roll back to 1.3.0" }));
    expect(rollbackApi.request).toHaveBeenCalledWith("shop", expect.objectContaining({ to: "dep_1.3.0" }));
  });

  it("shows an emergency on a protected environment as such", async () => {
    render(<RollbackControl action={{ ...available, emergency: true }} unit={unit} />);
    await userEvent.setup().click(screen.getByRole("button", { name: /Roll back to 1\.3\.2/ }));
    expect(screen.getByRole("dialog").textContent).toMatch(/emergency action on a protected environment.*needs no approval first.*approver reviews it afterwards/);
  });

  it("offers no working button when it is not available, and says why and what would help", () => {
    const a: ServerAction = { name: "rollback", available: false, who: "operator", reason: "no deploy runner is serving target homelab right now", would_help: "start or repair the deploy runner" };
    render(<RollbackControl action={a} unit={unit} />);
    expect(screen.getByRole("button", { name: "Roll back" })).toBeDisabled();
    expect(screen.getByText(/no deploy runner is serving target homelab/)).toBeInTheDocument();
    expect(screen.getByText(/start or repair the deploy runner/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Roll back to/ })).not.toBeInTheDocument();
  });

  it("offers to queue behind a running deployment, as the server says it can", async () => {
    const a: ServerAction = { ...available, available: false, queueable: true, reason: "run #3 is deploying shop to dev right now and holds its lock", instead: "queue the rollback behind run #3, or cancel run #3 first" };
    render(<RollbackControl action={a} unit={unit} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Queue the rollback behind run #3/ }));
    expect(screen.getByRole("dialog").textContent).toMatch(/waits for it and then goes first/);
    await user.type(screen.getByRole("textbox"), "urgent");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Roll back to 1\.3\.2/ }));
    expect(rollbackApi.request).toHaveBeenCalledWith("shop", expect.objectContaining({ queue: true }));
  });
});

describe("links and review", () => {
  it("says on the original run that a rollback undid it", () => {
    render(<RolledBackLine run={{ rolled_back_by: [{ id: "run_rb", number: 9, actor: "ann", reason: "checkout fails" }] } as unknown as RunDetail} />);
    expect(screen.getByRole("status").textContent).toMatch(/Rolled back later by run #9 \(ann: checkout fails\)/);
    expect(screen.getByRole("link", { name: "run #9" })).toHaveAttribute("href", "/runs/run_rb");
  });

  const item = (acknowledge: ServerAction): ReviewItem => ({ run: "run_rb", number: 9, project: "shop", environment: "prod", service: "shop", from_version: "1.4.0", to_version: "1.3.2",
    reason: "payments down", actor: "otto", at: Date.now() - 60_000, acknowledge });
  it("lists emergency rollbacks and lets an approver acknowledge one with a comment", async () => {
    const changed = vi.fn();
    render(<NeedsReview items={[item({ name: "acknowledge", available: true, who: "operator" })]} onChanged={changed} />);
    expect(screen.getByText(/rolled back from 1\.4\.0 to 1\.3\.2 by otto/)).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Acknowledge…" }));
    const ok = screen.getByRole("button", { name: "Acknowledge" });
    expect(ok).toBeDisabled();
    await user.type(screen.getByRole("textbox"), "checked the incident");
    await user.click(ok);
    expect(rollbackApi.review).toHaveBeenCalledWith("run_rb", "checked the incident");
    expect(changed).toHaveBeenCalled();
  });
  it("gives someone who may not acknowledge no button, only the reason", () => {
    render(<NeedsReview items={[item({ name: "acknowledge", available: false, who: "operator", reason: "you asked for this rollback, so someone else must review it" })]} onChanged={() => {}} />);
    expect(screen.queryByRole("button", { name: /Acknowledge/ })).not.toBeInTheDocument();
    expect(screen.getByText(/someone else must review it/)).toBeInTheDocument();
  });
  it("shows nothing when nothing waits", () => {
    const { container } = render(<NeedsReview items={[]} onChanged={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });
});
