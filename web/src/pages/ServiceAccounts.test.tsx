import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ServiceAccountsSection } from "./ServiceAccounts";

const calls: string[] = [];
beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname} ${init?.body ?? ""}`);
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname.endsWith("/tokens") && init?.method === "POST") return j({ id: "tok_1", token: "lw_svc-secret" }, 201);
    if (u.pathname === "/api/v1/service-accounts" && init?.method === "POST") return j({ name: "gitops", role: "viewer", disabled: false, created_at: 1 }, 201);
    if (u.pathname === "/api/v1/service-accounts") return j({ service_accounts: [{ name: "gitops", description: "controller", role: "viewer", disabled: false, created_at: 1 }] });
    return j({});
  }));
});
afterEach(() => vi.unstubAllGlobals());

describe("service accounts", () => {
  it("lists accounts, makes one, and switches one off", async () => {
    render(<ServiceAccountsSection />);
    await screen.findByText("gitops");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "mirror-job" } });
    fireEvent.click(screen.getByRole("button", { name: "Make service account" }));
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST /api/v1/service-accounts "))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Switch off" }));
    await waitFor(() => expect(calls).toContain('PATCH /api/v1/service-accounts/gitops {"disabled":true}'));
  });
  it("makes a token limited to repositories and shows it once", async () => {
    render(<ServiceAccountsSection />);
    await screen.findByText("gitops");
    fireEvent.click(screen.getByRole("button", { name: "Make token" }));
    fireEvent.change(screen.getByLabelText("Token name"), { target: { value: "sync" } });
    fireEvent.change(screen.getByLabelText("Repositories"), { target: { value: "acme/shop, acme/*" } });
    fireEvent.change(screen.getByLabelText("Only from addresses"), { target: { value: "203.0.113.0/24" } });
    fireEvent.click(screen.getByRole("button", { name: "Make token for gitops" }));
    await screen.findByDisplayValue("lw_svc-secret");
    expect(calls).toContain('POST /api/v1/service-accounts/gitops/tokens {"name":"sync","expires_in_days":90,"repositories":["acme/shop","acme/*"],"repo_access":"read","source_ranges":["203.0.113.0/24"]}');
  });
  it("says plainly what is wrong with a repository or a range", async () => {
    render(<ServiceAccountsSection />);
    await screen.findByText("gitops");
    fireEvent.click(screen.getByRole("button", { name: "Make token" }));
    fireEvent.change(screen.getByLabelText("Repositories"), { target: { value: "acme" } });
    expect(screen.getByRole("alert").textContent).toContain("is not a repository");
    expect((screen.getByRole("button", { name: "Make token for gitops" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
