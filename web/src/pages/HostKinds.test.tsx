import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPage } from "./Connections";
import { ProjectOutsidePage } from "./ProjectOutside";
import { HOST_KINDS, hostKind, webhookSteps } from "../hostKinds";
import { expectAccessible } from "../test/axe";

// The pages are the same for every kind of outside host: only its labels and setup steps differ. These tests run
// the same journeys for each kind listed and check the words each shows.

type Call = { method: string; url: string; body: unknown };
const calls: Call[] = [];
function serve(h: (method: string, url: string) => unknown) {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return new Response(JSON.stringify(h(method, url) ?? {}), { status: method === "POST" ? 201 : 200, headers: { "Content-Type": "application/json" } });
  }));
}
afterEach(() => vi.unstubAllGlobals());

describe.each(HOST_KINDS.filter((k) => k.value !== "github"))("the pages for $value", (k) => {
  it("offers the kind and adds a connection of it", async () => {
    serve((m) => (m === "POST" ? { name: "c", kind: k.value } : { items: [] }));
    const { container } = render(<ConnectionsPage />);
    await screen.findByText("No connection yet");
    const select = screen.getByLabelText("Kind of host") as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toContain(k.value);
    fireEvent.change(select, { target: { value: k.value } });
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "c" } });
    fireEvent.change(screen.getByLabelText(/Environment variable that holds the token/), { target: { value: "TOK" } });
    if (k.addressRequired) {
      const add = screen.getByRole("button", { name: "Add the connection" }) as HTMLButtonElement;
      expect(add.disabled).toBe(true); // an address is needed first
      fireEvent.change(screen.getByLabelText(/Address of the install/), { target: { value: "https://git.example.com" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Add the connection" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect((post.body as { kind: string }).kind).toBe(k.value);
    await expectAccessible(container, { name: `connections ${k.value}` });
  });

  it("shows the host's own words for its change request, its webhook steps and what it adds", async () => {
    const f = { project: "shop", connection: "c", repository: "acme/app", kind: k.value, report_statuses: true, report_comment: true,
      webhook_url: "https://lw.example.com/api/v1/outside-hosts/c/webhook",
      additions: [{ key: "auto_merge", title: "Auto-merge", what: "The host merges by itself", drivable: true }], unsupported: {} };
    serve((_m, u) => (u.includes("/outside") ? f : []));
    const { container } = render(<ProjectOutsidePage name="shop" />);
    await screen.findByText("acme/app");
    expect(screen.getByLabelText(new RegExp(`One summary comment on each ${k.change}`))).toBeTruthy();
    for (const step of webhookSteps(k)) expect(screen.getByText(step)).toBeTruthy();
    expect(screen.getByText(`What ${k.name} adds`)).toBeTruthy();
    await expectAccessible(container, { name: `flight ${k.value}` });
  });
});

describe("hostKind", () => {
  it("knows each listed kind and is silent about others", () => {
    for (const k of HOST_KINDS) expect(hostKind(k.value)).toBe(k);
    expect(hostKind("nope")).toBeUndefined();
  });
});
