import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Me } from "../api";
import { MeContext } from "../auth";
import { TokensPage } from "./Tokens";

const me = { user: "ada", groups: [], auth_mode: "oidc", via: "session", admin: true, runners: true, roles: {}, mcp: { enabled: false, path: "/mcp" }, hosting: true } as unknown as Me;
const calls: string[] = [];
beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname} ${init?.body ?? ""}`);
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname === "/api/v1/tokens" && init?.method === "POST") return j({ id: "tok_1", name: "mirror", token: "lw_value", created_at: 1, expires_at: 2, last_used: 0 }, 201);
    if (u.pathname === "/api/v1/tokens") return j([{ id: "tok_2", name: "pusher", created_at: 1, expires_at: 4102444800000, last_used: 0,
      repo_access: "write", repositories: [{ pattern: "acme/shop" }], source_ranges: ["203.0.113.0/24"] }]);
    if (u.pathname === "/api/v1/tokens/limits") return j({ max_days: 365, default_days: 90, your_role: "admin" });
    if (u.pathname === "/api/v1/repositories") return j({ items: [{ id: "r", path: "acme/shop", name: "shop", visibility: "private", clone: {} }] });
    if (u.pathname === "/api/v1/service-accounts") return j({ service_accounts: [] });
    return j([]);
  }));
});
afterEach(() => vi.unstubAllGlobals());
const page = () => render(<MeContext.Provider value={me}><TokensPage /></MeContext.Provider>);

describe("the tokens page", () => {
  it("shows a token's repositories, access and source addresses", async () => {
    page();
    await screen.findByText("pusher");
    expect(screen.getByText(/read and push: acme\/shop/)).toBeTruthy();
    expect(screen.getByText(/only from 203.0.113.0\/24/)).toBeTruthy();
  });
  it("makes a token for repositories with a source range, and refuses a bad pattern or range in words", async () => {
    page();
    await screen.findByText("pusher");
    const form = within(screen.getByRole("form", { name: "Make a token" }));
    fireEvent.change(form.getByLabelText("Name"), { target: { value: "mirror" } });
    fireEvent.change(form.getByLabelText("Repositories"), { target: { value: "acme" } });
    expect(screen.getByRole("alert").textContent).toContain("is not a repository");
    fireEvent.change(form.getByLabelText("Repositories"), { target: { value: "acme/shop" } });
    fireEvent.change(form.getByLabelText("Only from addresses"), { target: { value: "10.0.0.0/33" } });
    expect(screen.getByRole("alert").textContent).toContain("is not an address range");
    fireEvent.change(form.getByLabelText("Only from addresses"), { target: { value: "203.0.113.0/24" } });
    fireEvent.click(form.getByRole("button", { name: "Make token" }));
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST /api/v1/tokens ") && c.includes('"repositories":["acme/shop"]') && c.includes('"repo_access":"read"') && c.includes('"source_ranges":["203.0.113.0/24"]'))).toBe(true));
  });
  it("offers administrators the service accounts section", async () => {
    page();
    await screen.findByRole("heading", { name: "Service accounts" });
  });
});
