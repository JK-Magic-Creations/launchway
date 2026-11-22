import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SecurityPage } from "./SecurityPage";

const calls: string[] = [];
function serve(state: object) {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname}`);
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname.endsWith("/inspections") && init?.method === "POST") return j({ run: { id: "run_9", number: 9 }, project: "shop" }, 201);
    if (u.pathname.endsWith("/inspections")) return j(state);
    if (u.pathname.endsWith("/findings")) return j({ findings: [], matched: 0, by_severity: {}, by_check: {}, open: 0, accepted: 0, fixed: 0 });
    if (u.pathname.endsWith("/findings/history")) return j({ points: [], fixed_count: 0, mean_days_to_fix: 0, oldest_open: 0 });
    return j({}, 404);
  }));
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("the Security tab of a repository", () => {
  it("starts an Inspection and says where to read it", async () => {
    serve({ available: true, project: "shop", branch: "main", runs: [] });
    render(<main><SecurityPage path="acme/shop" /></main>);
    const b = await screen.findByRole("button", { name: "Inspect now" });
    await waitFor(() => expect((b as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(b);
    expect(await screen.findByText(/Started:/)).toBeTruthy();
    expect(calls).toContain("POST /api/v1/repositories/acme/shop/inspections");
    expect(await screen.findByText("Nothing open")).toBeTruthy(); // the findings of the project are listed below
  });
  it("disables the button and says why when no Station can take the run", async () => {
    serve({ available: false, reason: "No Station is online to take the run.", project: "shop", branch: "main", runs: [] });
    render(<main><SecurityPage path="acme/shop" /></main>);
    expect(await screen.findByText("No Station is online to take the run.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Inspect now" }) as HTMLButtonElement).disabled).toBe(true);
  });
  it("is honest when no project watches the repository", async () => {
    serve({ available: false, reason: "No project watches this repository, and an Inspection is a run.", runs: [] });
    render(<main><SecurityPage path="acme/shop" /></main>);
    expect(await screen.findByText("No findings yet")).toBeTruthy();
    expect(screen.getByText(/no Flight watches this repository yet/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Inspect now" }) as HTMLButtonElement).disabled).toBe(true);
    expect(calls.some((c) => c.includes("/findings"))).toBe(false);
  });
});
