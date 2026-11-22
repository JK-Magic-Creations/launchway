import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";
import { FindingCard, type Shown } from "./Inspection";

const f: Shown = { fingerprint: "fp1", check: "secrets", tool: "gitleaks", rule: "github-pat", severity: "high", kind: "secret", title: "A token", file: "docs/assertion_vectors.json", line: 3, status: "open" };
const calls: string[] = [];
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const open = () => {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${new URL(url, "http://x").pathname} ${init?.body ?? ""}`);
    return new Response(JSON.stringify({ decided: 52, status: "accepted" }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  render(<MeContext.Provider value={me({ admin: true, roles: { hub: "admin" } })}><ul><FindingCard f={f} project="hub" profile={null} onChanged={() => {}} /></ul></MeContext.Provider>);
  fireEvent.change(screen.getByLabelText("Why is this safe to live with?"), { target: { value: "test vectors, not secrets" } });
};

describe("accepting findings", () => {
  it("one finding, by its fingerprint", async () => {
    open();
    fireEvent.click(screen.getByRole("button", { name: "Record the decision" }));
    await waitFor(() => expect(calls[0]).toMatch(/^POST \/api\/v1\/projects\/hub\/findings\/fp1\/accept /));
  });
  it("every open finding of that kind in the folder, by its path", async () => {
    open();
    fireEvent.click(screen.getByLabelText(/The same for every open finding of this kind in/));
    fireEvent.click(screen.getByRole("button", { name: "Record the decision" }));
    await waitFor(() => expect(calls[0]).toMatch(/^POST \/api\/v1\/projects\/hub\/findings\/accept-path /));
    expect(calls[0]).toContain('"path":"docs/**"');
    expect(calls[0]).toContain('"check":"secrets"');
  });
});
