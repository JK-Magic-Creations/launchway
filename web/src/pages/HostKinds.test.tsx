import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPage } from "./Connections";
import { ProjectOutsidePage } from "./ProjectOutside";
import { hostKind, resetHostKinds, webhookSteps, type HostKind } from "../hostKinds";
import { expectAccessible } from "../test/axe";

// The pages are the same for every kind of outside host: only its labels and setup steps differ. These tests run
// the same journeys for each kind listed and check the words each shows.

type Call = { method: string; url: string; body: unknown };
const calls: Call[] = [];
function serve(h: (method: string, url: string) => unknown) {
  calls.length = 0;
  resetHostKinds();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (url.endsWith("/outside-host-matrix")) return new Response(JSON.stringify({ features: [], kinds: KINDS }), { status: 200, headers: { "Content-Type": "application/json" } });
    calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return new Response(JSON.stringify(h(method, url) ?? {}), { status: method === "POST" ? 201 : 200, headers: { "Content-Type": "application/json" } });
  }));
}
afterEach(() => vi.unstubAllGlobals());

// The kinds the server would serve in its matrix; the pages read them from there, so a test serves them.
const KINDS: HostKind[] = [
  { kind: "github", label: "GitHub", change: "pull request", capabilities: {} },
  { kind: "gitlab", label: "GitLab", change: "merge request", secret_field: "Secret token", no_content_type: true, capabilities: {} },
  { kind: "bitbucket", label: "Bitbucket Cloud", change: "pull request", no_content_type: true, capabilities: {} },
  { kind: "gitea", label: "Gitea", change: "pull request", address_required: true, capabilities: {} },
  { kind: "azuredevops", label: "Azure DevOps", change: "pull request", capabilities: {} },
];

describe.each(KINDS.filter((k) => k.kind !== "github"))("the pages for $kind", (k) => {
  it("offers the kind and adds a connection of it", async () => {
    serve((m) => (m === "POST" ? { name: "c", kind: k.kind } : { items: [] }));
    const { container } = render(<ConnectionsPage />);
    await screen.findByText("No connection yet");
    const select = screen.getByLabelText("Kind of host") as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toContain(k.kind);
    fireEvent.change(select, { target: { value: k.kind } });
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "c" } });
    fireEvent.change(screen.getByLabelText(/Environment variable that holds the token/), { target: { value: "TOK" } });
    if (k.address_required) {
      const add = screen.getByRole("button", { name: "Add the connection" }) as HTMLButtonElement;
      expect(add.disabled).toBe(true); // an address is needed first
      fireEvent.change(screen.getByLabelText(/Address of the install/), { target: { value: "https://git.example.com" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Add the connection" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect((post.body as { kind: string }).kind).toBe(k.kind);
    await expectAccessible(container, { name: `connections ${k.kind}` });
  });

  it("shows the host's own words for its change request, its webhook steps and what it adds", async () => {
    const f = { project: "shop", connection: "c", repository: "acme/app", kind: k.kind, report_statuses: true, report_comment: true,
      webhook_url: "https://lw.example.com/api/v1/outside-hosts/c/webhook",
      additions: [{ key: "auto_merge", title: "Auto-merge", what: "The host merges by itself", drivable: true }], unsupported: {} };
    serve((_m, u) => (u.includes("/outside") ? f : []));
    const { container } = render(<ProjectOutsidePage name="shop" />);
    await screen.findByText("acme/app");
    expect(await screen.findByLabelText(new RegExp(`One summary comment on each ${k.change}`))).toBeTruthy();
    for (const step of webhookSteps(k)) expect(screen.getByText(step)).toBeTruthy();
    expect(screen.getByText(`What ${k.label} adds`)).toBeTruthy();
    await expectAccessible(container, { name: `flight ${k.kind}` });
  });
});

describe("hostKind", () => {
  it("finds a served kind and is silent about others", () => {
    for (const k of KINDS) expect(hostKind(KINDS, k.kind)).toBe(k);
    expect(hostKind(KINDS, "nope")).toBeUndefined();
  });
});
