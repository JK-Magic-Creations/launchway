import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPage } from "./Connections";
import { ProjectOutsidePage } from "./ProjectOutside";
import { OutsidePicker } from "../components/OutsidePicker";
import { RunPullRequest } from "../components/RunPullRequest";
import { expectAccessible } from "../test/axe";
import { resetHostKinds } from "../hostKinds";

type Handler = (method: string, url: string, body: unknown) => { status?: number; body?: unknown };
const calls: { method: string; url: string; body: unknown }[] = [];
function serve(h: Handler) {
  resetHostKinds();
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const r = h(method, url, body);
    return new Response(r.status === 204 ? null : JSON.stringify(r.body ?? {}), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
}
afterEach(() => vi.unstubAllGlobals());

const gh = { name: "acme-gh", kind: "github", credential: "env:ACME_GH_TOKEN", webhook_secret: "env:ACME_GH_HOOK", auth_kind: "token", owners: ["acme"], enabled: true };

describe("the connections page", () => {
  it("shows what each host can do from the server's matrix, only once asked", async () => {
    const nat = { support: "native" }, none = { support: "none", reason: "The host has no such thing.", workaround: "Use the host's own page." };
    serve((_m, u) => u.endsWith("/outside-host-matrix")
      ? { body: { features: [{ key: "reviews", title: "Reviews" }], kinds: [{ kind: "acme", label: "Acme Git", capabilities: { reviews: none } }, { kind: "gh", label: "GitHub", capabilities: { reviews: nat } }] } }
      : { body: { items: [] } });
    const { container } = render(<ConnectionsPage />);
    await screen.findByText("No connection yet");
    expect(screen.queryByRole("columnheader", { name: "Acme Git" })).toBeNull(); // the table is shown only once asked
    fireEvent.click(screen.getByRole("button", { name: "What each host can do" }));
    await screen.findByRole("columnheader", { name: "Acme Git" });
    expect(screen.getByText("none")).toBeTruthy();
    expect(screen.getByText(/Use the host's own page\./)).toBeTruthy();
    await expectAccessible(container, { name: "host matrix" });
  });
  it("tests a connection and lists what the token has and lacks, and never shows a token", async () => {
    serve((_m, u) => {
      if (u.endsWith("/test")) return { body: { connection: "acme-gh", report: { account: "svc-launchway", rate_remaining: 4000, repositories: 3, checks: [
        { feature: "Commit statuses", needs: "Commit statuses: read and write", ok: true },
        { feature: "Releases and tags", needs: "Contents: read and write", ok: false, detail: "the token can push to none of the repositories it reaches" },
        { feature: "Webhooks", needs: "Admin on the repository", ok: false, not_checked: true, detail: "Not checked: the host's API does not say" }] } } };
      return { body: { items: [gh] } };
    });
    const { container } = render(<ConnectionsPage />);
    await screen.findByRole("heading", { name: /acme-gh/ });
    expect(screen.getByText("env:ACME_GH_TOKEN")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Test the connection" }));
    await screen.findByText("svc-launchway");
    expect(screen.getByText("Has it")).toBeTruthy();
    expect(screen.getByText(/Lacks it/)).toBeTruthy();
    expect(screen.getAllByText("Not checked").length).toBe(1); // never shown as "Has it"
    expect(screen.getAllByText("Has it").length).toBe(1);
    expect(screen.getByText("Contents: read and write")).toBeTruthy();
    await expectAccessible(container, { name: "connections" });
  });
  it("adds a connection with references to secrets, and removes one only after confirming", async () => {
    serve((m) => (m === "POST" ? { status: 201, body: gh } : m === "DELETE" ? { body: gh } : { body: { items: [] } }));
    render(<ConnectionsPage />);
    await screen.findByText("No connection yet");
    const add = screen.getByRole("button", { name: "Add the connection" }) as HTMLButtonElement;
    expect(add.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "acme-gh" } });
    fireEvent.change(screen.getByLabelText(/Environment variable that holds the token/), { target: { value: "ACME_GH_TOKEN" } });
    fireEvent.change(screen.getByLabelText(/Owners it may reach/), { target: { value: "acme, acme-labs" } });
    fireEvent.click(add);
    await screen.findByText(/Added acme-gh/);
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ name: "acme-gh", kind: "github", credential: { env: "ACME_GH_TOKEN" }, owners: ["acme", "acme-labs"] });
  });
  it("asks before removing", async () => {
    serve((m) => (m === "DELETE" ? { body: gh } : { body: { items: [gh] } }));
    render(<ConnectionsPage />);
    await screen.findByRole("heading", { name: /acme-gh/ });
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Yes, remove acme-gh" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.endsWith("/acme-gh"))).toBe(true));
  });
});

const attached = { project: "shop", connection: "acme-gh", repository: "acme/app", kind: "github", report_statuses: true, report_comment: false,
  webhook_url: "https://lw.example.com/api/v1/outside-hosts/acme-gh/webhook", last_delivery_at: Date.UTC(2026, 10, 20, 9, 30),
  additions: [{ key: "auto_merge", title: "Auto-merge", what: "The host merges a pull request once its checks pass", drivable: true }],
  unsupported: { rules: { support: "none", reason: "Launchway only reads branch protection through a connection", workaround: "Change it on the host." } } };

describe("the code host of a Flight", () => {
  it("shows the repository, the webhook address with a copy button, the last delivery and what the host cannot do", async () => {
    serve((_m, u) => (u.endsWith("/outside-host-matrix") ? { body: { features: [], kinds: [{ kind: "github", label: "GitHub", change: "pull request", capabilities: {} }] } } : u.includes("/projects/shop/outside") ? { body: attached } : { body: [] }));
    const { container } = render(<ProjectOutsidePage name="shop" />);
    await screen.findByText("acme/app");
    expect(screen.getByText("https://lw.example.com/api/v1/outside-hosts/acme-gh/webhook")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Copy: Address to call/ })).toBeTruthy();
    expect(screen.getByText(/Last delivery received/)).toBeTruthy();
    expect(screen.getByText(/This host cannot change branch protection/)).toBeTruthy();
    expect(screen.getByText(/Change it on the host\./)).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "What GitHub adds" })).toBeTruthy();
    expect(screen.getByText(/Launchway can switch it on for you/)).toBeTruthy();
    await expectAccessible(container, { name: "code host" });
  });
  it("saves a report switch", async () => {
    serve((m, u) => (m === "PUT" ? { body: { ...attached, report_comment: true } } : u.includes("/projects/shop/outside") ? { body: attached } : { body: [] }));
    render(<ProjectOutsidePage name="shop" />);
    await screen.findByText("acme/app");
    fireEvent.click(screen.getByLabelText(/One summary comment/));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ report_comment: true }));
  });
  it("offers to attach a repository found through a connection when none is attached", async () => {
    serve((m, u) => {
      if (m === "PUT") return { body: attached };
      if (u.includes("/api/v1/runs")) return { body: [] };
      if (u.includes("/repositories")) return { body: { items: [{ id: "r1", path: "acme/app", name: "app", visibility: "private", clone: { https: "https://git.example.com/acme/app.git" } }] } };
      if (u.includes("/projects/shop/outside")) return { status: 404, body: { error: "not attached" } };
      return { body: { items: [gh] } };
    });
    render(<ProjectOutsidePage name="shop" />);
    await screen.findByRole("heading", { name: "Attach a repository" });
    fireEvent.click(await screen.findByRole("button", { name: "Search the host" }));
    fireEvent.click(await screen.findByRole("button", { name: "Attach acme/app" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ connection: "acme-gh", repository: "acme/app" }));
  });
});

describe("choosing a repository on a host in the wizard", () => {
  it("draws nothing when there is no connection", async () => {
    serve(() => ({ body: { items: [] } }));
    const { container } = render(<OutsidePicker picked={null} onPick={() => {}} />);
    await waitFor(() => expect(calls.length).toBe(1));
    expect(container.textContent).toBe("");
  });
  it("searches through the connection and hands back the clone address", async () => {
    serve((_m, u) => (u.includes("/repositories") ? { body: { items: [{ id: "r1", path: "acme/app", name: "app", visibility: "private", default_branch: "main", clone: { https: "https://git.example.com/acme/app.git" } }] } } : { body: { items: [gh] } }));
    const picked = vi.fn();
    const { container } = render(<OutsidePicker picked={null} onPick={picked} />);
    await screen.findByLabelText(/Search the host/);
    fireEvent.change(screen.getByLabelText(/Search the host/), { target: { value: "app" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: "Use acme/app" }));
    expect(picked).toHaveBeenCalledWith({ connection: "acme-gh", repository: "acme/app", address: "https://git.example.com/acme/app.git" });
    expect(calls.some((c) => c.url.includes("q=app"))).toBe(true);
    await expectAccessible(container, { name: "picker" });
  });
});

describe("the pull request on the run page", () => {
  it("says Pull request #N: title, by author, with a link to the host", () => {
    render(<RunPullRequest run={{ pull_request: 12, trigger_detail: { pull_request_title: "Fix the retry", pull_request_author: "dana", pull_request_url: "https://git.example.com/acme/app/pull/12", pull_request_fork: true } }} />);
    const a = screen.getByRole("link", { name: /Pull request #12: Fix the retry, by dana/ });
    expect(a.getAttribute("href")).toBe("https://git.example.com/acme/app/pull/12");
    expect(screen.getByText(/from a fork: restricted/)).toBeTruthy();
  });
  it("draws nothing for a run of a hosted repository, and never links an address that is not http(s)", () => {
    const { container, rerender } = render(<RunPullRequest run={{ pull_request: 3 }} />);
    expect(container.textContent).toBe("");
    rerender(<RunPullRequest run={{ pull_request: 3, trigger_detail: { pull_request_title: "x", pull_request_url: "javascript:alert(1)" } }} />);
    expect(container.querySelector("a")).toBeNull();
  });
});
