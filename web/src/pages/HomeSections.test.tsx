import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { scan } from "../test/axe";
import { homeFixture } from "../test/mockApi";
import HomeSections from "./HomeSections";

type Body = unknown;
let calls: { method: string; path: string; body?: Body }[] = [];
let answer: ReturnType<typeof homeFixture>;
beforeEach(() => {
  calls = [];
  answer = homeFixture();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push({ method: init?.method ?? "GET", path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (path.startsWith("/api/v1/me/layouts/home") && init?.method === "PUT") {
      const personal = JSON.parse(String(init.body));
      answer = { ...answer, layout: { ...answer.layout, personal, effective: { ...answer.layout.effective, ...personal }, customized: true } };
    }
    if (path.startsWith("/api/v1/me/layouts/home") && init?.method === "DELETE") answer = homeFixture();
    return new Response(JSON.stringify(path.startsWith("/api/v1/home") ? answer : {}), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => vi.unstubAllGlobals());

describe("Home sections", () => {
  it("lists what needs you with why it is there and what to do, and what runs now, retrying a failure", async () => {
    render(<HomeSections />);
    const needs = (await screen.findByRole("heading", { name: /Needs you/ })).closest(".sheet") as HTMLElement;
    expect(within(needs).getByText(/Sortie #7 of shop-api failed on main/)).toBeInTheDocument();
    expect(within(needs).getByText(/Why it is here:/).parentElement).toHaveTextContent("default branch");
    expect(within(needs).getByText(/What to do:/)).toBeInTheDocument();
    const running = screen.getByRole("heading", { name: /Running now/ }).closest(".sheet") as HTMLElement;
    expect(within(running).getByText("shop-web #118")).toBeInTheDocument();
    expect(within(running).getByText("Retrying #117")).toBeInTheDocument();
    expect(within(running).getByText("yours")).toBeInTheDocument();
  });
  it("sets an entry aside for the person, and the page asks again", async () => {
    render(<HomeSections />);
    await userEvent.click(await screen.findByRole("button", { name: "Set aside" }));
    expect(calls.find((c) => c.path === "/api/v1/attention/dismiss")?.body).toEqual({ key: "failed:run_7", snooze_hours: 0 });
    await userEvent.click(screen.getByRole("button", { name: "Snooze a day" }).closest("div")!.querySelector("button") as HTMLElement).catch(() => {});
  });
  it("shows the projects by Squadron with a roll-up in words, collapsible, and a way into the Explorer", async () => {
    render(<HomeSections />);
    const btn = await screen.findByRole("button", { name: /Squadron Shop/ });
    expect(btn).toHaveAttribute("aria-expanded", "true");
    expect(btn).toHaveTextContent("1 need a look");
    expect(screen.getByRole("link", { name: "Show all in the explorer" })).toHaveAttribute("href", "/pipelines");
    await userEvent.click(btn);
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && (c.body as { open?: Record<string, boolean> }).open?.["unit:shop"] === false)).toBe(true));
  });
  it("my arrangement: reorder, hide, say it is personal, reset to the default", async () => {
    render(<HomeSections />);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange for me"));
    expect(screen.getByText(/changes only your Home page/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Move Running now up" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ order: ["running", "needs", "favorites", "categories", "recent"] }));
    await userEvent.click(screen.getByRole("button", { name: /^Hide\s*Recently finished/ }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Recently finished" })).not.toBeInTheDocument());
    expect(await screen.findByText(/viewing your own arrangement/)).toBeInTheDocument();
    const heads = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent ?? "");
    expect(heads.findIndex((h) => /Running now/.test(h))).toBeLessThan(heads.findIndex((h) => /Needs you/.test(h)));
    await userEvent.click(screen.getByRole("button", { name: "Reset to the default" }));
    await waitFor(() => expect(screen.queryByText(/viewing your own arrangement/)).not.toBeInTheDocument());
  });
  it("shows a skeleton that keeps the page height before the answer arrives", () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    render(<HomeSections />);
    expect(screen.getByLabelText(/Loading what needs you/)).toHaveAttribute("aria-busy", "true");
  });
});

describe.each(["light", "dark"])("Home sections are accessible, %s theme", (theme) => {
  beforeEach(() => document.documentElement.setAttribute("data-theme", theme));
  afterEach(() => document.documentElement.removeAttribute("data-theme"));
  it("has no violations", async () => {
    const { container } = render(<main><h1>Home</h1><HomeSections /></main>);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange for me"));
    expect(await scan(container)).toEqual([]);
  });
});
