import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MergeRulesPanel } from "./MergeRules";
import { PullList } from "./PullPeople";
import { pullFixtures } from "../test/mockApi";

const rules = "/api/v1/repositories/acme/shop/merge-rules";

function serve(editable: boolean) {
  const fx = pullFixtures() as Record<string, { actions?: { name: string; available: boolean; reason?: string }[] }>;
  if (!editable) fx[rules].actions = fx[rules].actions!.map((a) => (a.name === "require_signed_commits" ? a : { ...a, available: false, reason: "you need the admin role on acme/shop" }));
  const calls: { method: string; url: string; body?: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ method: init?.method ?? "GET", url, body: init?.body as string | undefined });
    const body = fx[url.split("?")[0]];
    return new Response(JSON.stringify(init?.method === "PUT" ? { rules: (fx[rules] as unknown as { rules: unknown[] }).rules, actions: fx[rules].actions, ...JSON.parse(String(init.body)) } : body), { status: 200 });
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("merge rules and pull request settings", () => {
  it("shows each rule's values and saves a changed rule", async () => {
    const calls = serve(true);
    render(<MergeRulesPanel path="acme/shop" />);
    const form = await screen.findByRole("form", { name: "Merge rule for main" });
    expect((within(form).getByLabelText("Approvals needed") as HTMLInputElement).value).toBe("1");
    expect((within(form).getByLabelText(/Checks that must have passed/) as HTMLInputElement).value).toBe("ci/test");
    await userEvent.click(within(form).getByLabelText(/Every review thread must be resolved/));
    await userEvent.click(within(form).getByRole("button", { name: /Save the rule for main/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.endsWith("/merge-rules"))).toBe(true));
    const put = calls.find((c) => c.method === "PUT" && c.url.endsWith("/merge-rules"))!;
    expect(JSON.parse(put.body!).rules[0]).toMatchObject({ pattern: "main", require_resolved_threads: true, require_up_to_date: true });
  });
  it("offers no switch for signed commits and says why", async () => {
    serve(true);
    render(<MergeRulesPanel path="acme/shop" />);
    expect(await screen.findByText(/Signed commits cannot be required yet/)).toBeTruthy();
    expect(screen.queryByLabelText(/signed/i)).toBeNull();
  });
  it("only reads when the server says the person may not edit, with the reason and no save buttons", async () => {
    serve(false);
    render(<MergeRulesPanel path="acme/shop" />);
    await screen.findByRole("form", { name: "Merge rule for main" });
    expect(screen.getAllByText(/you need the admin role on acme\/shop/).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /Save/ })).toBeNull();
    expect(screen.getByLabelText("Approvals needed").matches(":disabled")).toBe(true);
  });
  it("saves the settings: methods, the default and the branch and approval behaviour", async () => {
    const calls = serve(true);
    render(<MergeRulesPanel path="acme/shop" />);
    const form = await screen.findByRole("form", { name: "Pull request settings" });
    await userEvent.click(within(form).getByLabelText("Rebase and merge"));
    await userEvent.click(within(form).getByLabelText(/Delete the source branch/));
    await userEvent.click(within(form).getByRole("button", { name: "Save the settings" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.endsWith("/pull-settings"))).toBe(true));
    expect(JSON.parse(calls.find((c) => c.url.endsWith("/pull-settings") && c.method === "PUT")!.body!)).toMatchObject({ allow_rebase: true, delete_branch_after_merge: true, dismiss_stale_approvals: true });
  });
});

describe("the editable lists of a pull request", () => {
  it("removes an item and adds one, and offers neither when it may not edit", async () => {
    const add = vi.fn(async () => {}), remove = vi.fn(async () => {});
    const { rerender } = render(<PullList title="Reviewers" noun="reviewer" none="None asked" items={["grace"]} editable onAdd={add} onRemove={remove} />);
    await userEvent.click(screen.getByRole("button", { name: "Remove grace from reviewers" }));
    expect(remove).toHaveBeenCalledWith("grace");
    await userEvent.type(screen.getByLabelText("Add a reviewer"), "linus");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(add).toHaveBeenCalledWith("linus");
    rerender(<PullList title="Reviewers" noun="reviewer" none="None asked" items={["grace"]} editable={false} onAdd={add} onRemove={remove} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("grace")).toBeTruthy();
  });
});
