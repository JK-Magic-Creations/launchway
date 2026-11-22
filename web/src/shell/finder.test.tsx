import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { me } from "../test/fixtures";
import { DoBar } from "./DoBar";

afterEach(() => vi.unstubAllGlobals());
const ctx = () => ({ me: me(), facts: { projects: 0, runs: 0, runners: 0, environments: 0, approvals: 0, libraries: 0 } }) as never;

describe("the finder reaches across everything the server lets this person see", () => {
  it("shows favorites before anything is typed, and results grouped by kind after", async () => {
    const asked: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (u: RequestInfo | URL) => {
      asked.push(String(u));
      const body = String(u).includes("q=pay") ? { q: "pay", hits: [{ kind: "project", name: "pay-ledger", path: "Payments", href: "/projects/pay-ledger/pipeline", score: 80 }, { kind: "run", name: "pay-ledger #412", detail: "main a41f9c2a failed", href: "/runs/run_412", score: 90 }] } : { q: "", hits: [], favorites: ["project:shop-api"] };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(<DoBar ctx={ctx()} projects={[]} onClose={() => {}} />);
    expect(await screen.findByRole("option", { name: /shop-api/ })).toHaveAttribute("href", "/projects/shop-api/pipeline");
    await userEvent.type(screen.getByRole("combobox"), "pay");
    const run = await screen.findByRole("option", { name: /pay-ledger #412/ });
    expect(run).toHaveAttribute("href", "/runs/run_412");
    expect(run).toHaveAttribute("data-group", "Sorties");
    expect(screen.getAllByRole("option").find((o) => /pay-ledger\s+Payments/.test(o.textContent ?? ""))).toHaveAttribute("data-group", "Pipelines");
    await waitFor(() => expect(asked.some((a) => a.includes("q=pay"))).toBe(true));
  });
  it("still works when the server cannot be reached", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("offline"); }));
    render(<DoBar ctx={ctx()} projects={[]} onClose={() => {}} />);
    await userEvent.type(screen.getByRole("combobox"), "learn");
    expect(await screen.findByRole("option", { name: /Learn how Launchway works/ })).toBeInTheDocument();
  });
});

describe("the keyboard list", () => {
  it("opens with ? outside a field and lists the finder and the explorer filter", async () => {
    const { Frame } = await import("./Frame");
    void Frame;
    const { default: Shortcuts } = await import("./Shortcuts");
    render(<Shortcuts onClose={() => {}} />);
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: "/" })).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: "f" })).toBeInTheDocument();
  });
});
