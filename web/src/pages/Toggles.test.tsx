import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";

const calls: { method: string; path: string; body?: string }[] = [];
function watch() {
  const inner = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ method: init?.method ?? "GET", path: String(input), body: init?.body as string | undefined });
    return inner(input, init);
  }));
}
async function open(path: string) {
  history.replaceState(null, "", path);
  render(<App />);
  await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
}

describe("the toggles pages", () => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; calls.length = 0; installServer(); watch(); });
  afterEach(() => { vi.unstubAllGlobals(); });

  it("lists each toggle with what it does in every environment, and says what waits and what may be finished", async () => {
    await open("/projects/shop-api/toggles");
    const table = await screen.findByRole("table", {}, { timeout: 8000 });
    expect(within(table).getByRole("link", { name: "checkout-v2" })).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "dev" })).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "prod" })).toBeTruthy();
    expect(within(table).getByText(/1 rule\(s\), then 25% true, 75% false/)).toBeTruthy();
    expect(within(table).getByText(/1 change waiting for approval/)).toBeTruthy();
    expect(screen.getByText(/may have done its job: legacy-banner/)).toBeTruthy();
    expect(screen.getByText(/Launchway never removes or changes a toggle by itself/)).toBeTruthy();
    // The change that waits is offered to someone else to approve.
    expect(screen.getByRole("heading", { name: "Waiting for approval" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Approve" })).toBeTruthy();
  });

  it("the file panel compares a file with what is live before anything is applied", async () => {
    const user = userEvent.setup();
    await open("/projects/shop-api/toggles");
    await user.type(await screen.findByLabelText("The file", {}, { timeout: 8000 }), "toggles: {{}");
    await user.click(screen.getByRole("button", { name: "Show the differences" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/toggle-plan"))).toBe(true));
    expect(calls.some((c) => c.path.endsWith("/toggle-apply"))).toBe(false);
  }, 20000);

  it("approving sends the call and nothing else", async () => {
    await open("/projects/shop-api/toggles");
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }, { timeout: 8000 }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/toggle-changes/12/approve"))).toBe(true));
  });

  it("the rules editor is operable by keyboard and sends the state it shows with the reason", async () => {
    const user = userEvent.setup();
    await open("/projects/shop-api/toggles/checkout-v2");
    await user.click((await screen.findAllByRole("button", { name: "Edit" }, { timeout: 8000 }))[0]!);
    await user.click(await screen.findByRole("button", { name: "Add a rule" }));
    expect(screen.getByRole("group", { name: /^Rule 2:/ })).toBeTruthy();
    // Move the new rule up with the keyboard: Tab to the button and press Enter.
    const up = screen.getByRole("button", { name: "Move rule 2 up" });
    up.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("group", { name: /^Rule 1:/ })).toBeTruthy();
    await user.type(screen.getByLabelText(/Why \(recorded/), "try a pro rule");
    await user.click(screen.getByRole("button", { name: "Save the change" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.path.includes("/toggles/checkout-v2/environments/dev"))).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    const body = JSON.parse(put.body!);
    expect(body.reason).toBe("try a pro rule");
    expect(body.state.rules).toHaveLength(2);
    expect(body.state.rules[0].serve).toEqual({ value: true }); // the new rule moved to the top
  }, 20000);

  it("history offers a revert that asks why, and says what a revert is", async () => {
    const user = userEvent.setup();
    await open("/projects/shop-api/toggles/checkout-v2");
    const buttons = await screen.findAllByRole("button", { name: "Revert" }, { timeout: 8000 });
    await user.click(buttons[0]!);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/never held for approval/)).toBeTruthy();
    const ok = within(dialog).getByRole("button", { name: "Revert" });
    expect((ok as HTMLButtonElement).disabled).toBe(true); // a reason first
    await user.type(within(dialog).getByLabelText("Why"), "wrong rule");
    await user.click(ok);
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.includes("/toggle-changes/") && c.path.endsWith("/revert"))).toBe(true));
  }, 20000);

  it("the page of where it is evaluated says what is not stored", async () => {
    await open("/projects/shop-api/toggles/checkout-v2");
    expect(await screen.findByText(/Nothing about who was evaluated is stored/, {}, { timeout: 8000 })).toBeTruthy();
    expect(screen.getByText(/1,200 evaluations in the last 30 days/)).toBeTruthy();
  });

  it("a kill switch has a prominent control that serves the safe value and says it is never held", async () => {
    const user = userEvent.setup();
    await open("/projects/shop-api/toggles/payments-kill");
    await user.click(await screen.findByRole("button", { name: "Switch to the safe value now" }, { timeout: 8000 }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/never held for an approval/)).toBeTruthy();
    await user.type(within(dialog).getByLabelText("Why"), "the provider is down");
    await user.click(within(dialog).getByRole("button", { name: "Switch it now" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.path.endsWith("/toggles/payments-kill/environments/prod"))).toBe(true));
    const body = JSON.parse(calls.find((c) => c.method === "PUT")!.body!);
    expect(body.state).toEqual({ on: true, off_value: false, fallthrough: { value: false } });
  }, 20000);

  it("the settings page shows the safeguards with where each comes from, and the key's prefix, never a secret", async () => {
    await open("/projects/shop-api/toggle-settings");
    expect(await screen.findByRole("table", {}, { timeout: 8000 })).toBeTruthy();
    expect(screen.getAllByText("default").length).toBeGreaterThan(0);
    expect(screen.getByText("lwt_srv_a1b2c3_...")).toBeTruthy();
  });

  it("the environment page shows deployments and toggle changes together", async () => {
    await open("/deployments/environments/prod");
    expect(await screen.findByRole("heading", { name: "Deployments and toggle changes together" }, { timeout: 8000 })).toBeTruthy();
    expect(screen.getAllByText("checkout-v2").length).toBeGreaterThan(0);
  });

  it("the rollback confirmation lists the toggle changes and reverts them only when asked", async () => {
    const user = userEvent.setup();
    await open("/deployments/environments/prod");
    await user.click(await screen.findByRole("button", { name: /Return to base: 1\.3\.2/ }, { timeout: 8000 }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/Toggle changes since 1\.3\.2 went live/)).toBeTruthy();
    const box = within(dialog).getByRole("checkbox", { name: /Also revert these toggle changes/ }) as HTMLInputElement;
    expect(box.checked).toBe(false);
    await user.type(within(dialog).getByRole("textbox"), "checkout is down");
    await user.click(box);
    await user.click(within(dialog).getByRole("button", { name: /^Return to base: 1\.3\.2$/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/rollbacks"))).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST" && c.path.endsWith("/rollbacks"))!.body!).revert_toggles).toBe(true);
  });
});
