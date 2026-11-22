import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RepoReach } from "./RepoReach";
import type { HostedRepo } from "../forgeApi";

const repo: HostedRepo = { id: "rep_1", path: "acme/shop", home: "acme", name: "shop", default_branch: "main", visibility: "private", clone: {} };
const calls: string[] = [];
beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname} ${init?.body ?? ""}`);
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname.endsWith("/credentials") && init?.method === "POST") return j({ id: "tok_1", token: "lw_secret-value" }, 201);
    if (u.pathname.endsWith("/credentials")) return j({ people: [{ who: "pay-ops", kind: "group", role: "operator" }],
      tokens: [{ id: "tok_9", name: "mirror", owner: "service:repo-acme-shop-read", access: "read", pattern: "acme/*", expires_at: 4102444800000, last_used: 0, source_ranges: ["203.0.113.0/24"] }] });
    if (u.pathname.endsWith("/deploy-keys")) return j({ items: [{ id: "k1", kind: "deploy", name: "ci box", type: "ed25519", fingerprint: "SHA256:x", public_key: "", can_write: false, created_at: "" }] });
    if (u.pathname.endsWith("/mirrors")) return j({ items: [{ id: "m1", direction: "push", url: "https://backup.example.com/shop.git" }] });
    return j({});
  }));
});
afterEach(() => vi.unstubAllGlobals());

describe("who can reach a repository", () => {
  it("lists people, deploy keys, tokens whose limits include it, and mirrors", async () => {
    render(<RepoReach repo={repo} />);
    await screen.findByText(/pay-ops/);
    expect(screen.getByText(/can read and push it/)).toBeTruthy();
    expect(screen.getByText("ci box")).toBeTruthy();
    expect(screen.getByText(/through/).textContent).toContain("acme/*");
    expect(screen.getByText(/only from 203.0.113.0\/24/)).toBeTruthy();
    expect(screen.getByText(/backup.example.com/)).toBeTruthy();
  });
  it("makes a credential for this repository alone in one step and shows it once", async () => {
    render(<RepoReach repo={repo} />);
    await screen.findByText(/pay-ops/);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "build system" } });
    fireEvent.change(screen.getByLabelText("Access"), { target: { value: "write" } });
    fireEvent.change(screen.getByLabelText("Only from addresses"), { target: { value: "203.0.113.0/24" } });
    fireEvent.click(screen.getByRole("button", { name: "Make credential" }));
    await screen.findByDisplayValue("lw_secret-value");
    expect(calls).toContain('POST /api/v1/repositories/acme/shop/credentials {"name":"build system","access":"write","expires_in_days":90,"source_ranges":["203.0.113.0/24"]}');
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByDisplayValue("lw_secret-value")).toBeNull();
  });
  it("refuses a bad address range before asking the server", async () => {
    render(<RepoReach repo={repo} />);
    await screen.findByText(/pay-ops/);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "x" } });
    fireEvent.change(screen.getByLabelText("Only from addresses"), { target: { value: "nonsense" } });
    expect(screen.getByRole("alert").textContent).toContain("is not an address range");
    expect((screen.getByRole("button", { name: "Make credential" }) as HTMLButtonElement).disabled).toBe(true);
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST"))).toBe(false));
  });
});
