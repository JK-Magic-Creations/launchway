import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { MergeRulesPanel } from "../pages/MergeRules";
import { installServer, pullFixtures } from "../test/mockApi";
import { scan } from "../test/axe";

// What review depth added to the pull request page: the merge queue box, the owners and viewed marks on the
// files, the shortcut list, and the merge rules and settings with the new switches. Both themes, an empty baseline.
const base = "/api/v1/repositories/acme/shop";
const queued = () => {
  const f = pullFixtures() as Record<string, Record<string, unknown> & { actions: unknown[]; readiness: object }>;
  const pull = { ...f[`${base}/pulls/1`] };
  pull.readiness = { ...pull.readiness, queue: true, needs_owner_approval: [{ path: "price.go", owners: ["ada", "group:finance"] }] };
  pull.queue = { pull_number: 1, position: 2, state: "testing", method: "merge", runs: ["run_7"] };
  pull.requested_reviewer_groups = ["finance"];
  pull.actions = [...pull.actions, { name: "dequeue", available: true }, { name: "update_branch", available: true }, { name: "request_review", available: true }];
  return { [`${base}/pulls/1`]: pull, [`${base}/pulls/1/queue`]: { enabled: true, branch: "main", entry: pull.queue, queue: [pull.queue] } };
};

describe.each(["light", "dark"])("Review depth views, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  const expectClean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };
  const open = async () => {
    history.replaceState(null, "", "/repositories/acme/shop/pulls/1");
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };

  it("the merge queue box, with its place and the group asked to review", async () => {
    installServer(queued());
    await open();
    await screen.findByText(/Number 2 in the merge queue for main, being tested/, {}, { timeout: 8000 });
    expect(screen.getByRole("button", { name: "Take out of the merge queue" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Update the branch" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Merge$/ })).toBeNull();
    await screen.findByText("finance");
    await expectClean("queue box");
  }, 20000);

  it("the files with their owners and viewed marks, and the shortcut list on ?", async () => {
    installServer();
    await open();
    fireEvent.click(await screen.findByRole("tab", { name: /Files changed/ }, { timeout: 8000 }));
    await screen.findByText(/Owned by @ada, group finance/, {}, { timeout: 8000 });
    expect(screen.getByRole("checkbox", { name: "Viewed price.go" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "?" });
    await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    await expectClean("owners, viewed and shortcuts");
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).toBeNull();
  }, 20000);

  it("the merge rules with owner approval, the queue and checks by path", async () => {
    installServer();
    render(<main><h1>Merge rules</h1><MergeRulesPanel path="acme/shop" /></main>);
    await screen.findByLabelText(/An owner of every changed file must approve/, {}, { timeout: 8000 });
    expect(screen.getByLabelText(/Merge through the merge queue/)).toBeTruthy();
    expect(screen.getByLabelText(/Checks required only when matching files change/)).toBeTruthy();
    expect(screen.getByLabelText(/Ask reviewers automatically/)).toBeTruthy();
    await expectClean("merge rules");
  }, 20000);
});
