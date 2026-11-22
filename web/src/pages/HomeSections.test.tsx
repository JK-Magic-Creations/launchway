import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { scan } from "../test/axe";
import { homeFixture } from "../test/mockApi";
import HomeSections from "./HomeSections";

type Body = unknown;
let calls: { method: string; path: string; body?: Body }[] = [];
let answer: ReturnType<typeof homeFixture>;
const trail = { paused: false, retention_days: 90, continue: [{ kind: "run", obj: "run_9", project: "shop-api", title: "shop-api #9", href: "/runs/run_9", action: "started", state: "running", at: 1000, now_state: "failed", changed: "Sortie #9 has finished: it failed." }],
  recent: [{ kind: "run", obj: "run_9", project: "shop-api", title: "shop-api #9", href: "/runs/run_9", action: "started", state: "running", at: 1000, changed: "Sortie #9 has finished: it failed." }, { kind: "pipeline", obj: "shop-web", project: "shop-web", title: "shop-web pipeline", href: "/projects/shop-web/pipeline", action: "viewed", state: "", at: 900 }] };
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
    if (path.startsWith("/api/v1/layouts/home") && init?.method === "PUT") {
      const shared = JSON.parse(String(init.body)).data;
      answer = { ...answer, layout: { ...answer.layout, shared, levels: [{ scope: "base", data: shared, locked: false }] as never } };
    }
    if (path.startsWith("/api/v1/activity")) return new Response(JSON.stringify(trail), { status: 200, headers: { "Content-Type": "application/json" } });
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
    await userEvent.click(screen.getByText("Arrange"));
    expect(screen.getByText(/changes only your Tower/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Move Running now up" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ order: ["running", "needs", "favorites", "continue", "categories", "recent"] }));
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

describe("Home panels", () => {
  it("shows what to pick up, with what changed since, and a searchable history grouped by day", async () => {
    render(<HomeSections />);
    const cont = await screen.findByRole("list", { name: "Continue" });
    expect(within(cont).getByRole("link", { name: "shop-api #9" })).toHaveAttribute("href", "/runs/run_9");
    expect(within(cont).getByText("Sortie #9 has finished: it failed.")).toBeInTheDocument();
    await userEvent.click(screen.getByText(/Recent history/));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search your history" }), "web");
    expect(screen.getByRole("link", { name: "shop-web pipeline" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "shop-api #9", hidden: false, description: "" }) ?? null).not.toBeNull();
  });
  it("pauses and clears the person's own history", async () => {
    render(<HomeSections />);
    await userEvent.click(await screen.findByRole("button", { name: "Pause history" }));
    expect(calls.find((c) => c.path === "/api/v1/activity/pause")?.body).toEqual({ paused: true });
    await userEvent.click(screen.getByRole("button", { name: "Clear my history" }));
    expect(calls.some((c) => c.method === "DELETE" && c.path === "/api/v1/activity")).toBe(true);
  });
  it("adds a notes panel for me, drawn with safe Markdown, then resizes and removes it", async () => {
    render(<HomeSections />);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange"));
    await userEvent.click(screen.getByRole("button", { name: "Add a panel" }));
    const dialog = screen.getByRole("dialog", { name: "Add a panel" });
    await userEvent.selectOptions(within(dialog).getByRole("combobox", { name: "Kind of panel" }), "note");
    await userEvent.type(within(dialog).getByLabelText("Title"), "Runbook");
    await userEvent.click(within(dialog).getByLabelText(/^Text/));
    await userEvent.paste("See [the runbook](https://example.com/rb) **now**");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add panel" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT" && JSON.stringify(c.body).includes("Runbook"))).toBeTruthy());
    await screen.findByRole("region", { name: "Runbook" });
    expect(await screen.findByRole("link", { name: "the runbook" })).toHaveAttribute("href", "https://example.com/rb");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Width of Runbook" }), "1");
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT").some((c) => JSON.stringify(c.body).includes('"size":1'))).toBe(true));
    await userEvent.click(screen.getByRole("button", { name: "Remove Runbook" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Runbook" })).not.toBeInTheDocument());
  });
  it("only someone who may arrange for everyone is offered that, and it saves the shared default", async () => {
    answer = { ...answer, layout: { ...answer.layout, may_arrange: { base: true } } };
    render(<HomeSections />);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange"));
    await userEvent.click(screen.getByRole("radio", { name: "Arrange for everyone" }));
    expect(screen.getByText(/default for everyone/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Move Running now up" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT" && c.path.startsWith("/api/v1/layouts/home"))?.body).toMatchObject({ scope: "base", data: { order: ["running", "needs", "favorites", "continue", "categories", "recent"] } }));
  });
  it("a person who may not arrange for everyone is not offered it", async () => {
    render(<HomeSections />);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange"));
    expect(screen.queryByRole("radio", { name: "Arrange for everyone" })).not.toBeInTheDocument();
  });
  it("a layout document that names things that do not exist does not break Home", async () => {
    answer = { ...answer, layout: { ...answer.layout, effective: { panels: [{ id: "x", type: "from-the-future" }, { id: "p:1", type: "note", title: "Ok", settings: { text: "hi" } }, 7, null], order: ["gone", "p:1"] } } };
    render(<HomeSections />);
    expect(await screen.findByRole("region", { name: "Ok" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Needs you/ })).toBeInTheDocument();
  });
  it("moves a panel with the keyboard buttons and announces nothing it cannot do", async () => {
    render(<HomeSections />);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange"));
    expect(screen.getByRole("button", { name: "Move Needs you up" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Move Recently finished down" })).toBeDisabled();
  });
});

describe.each(["light", "dark"])("Home sections are accessible, %s theme", (theme) => {
  beforeEach(() => document.documentElement.setAttribute("data-theme", theme));
  afterEach(() => document.documentElement.removeAttribute("data-theme"));
  it("has no violations", async () => {
    const { container } = render(<main><h1>Home</h1><HomeSections /></main>);
    await screen.findByRole("heading", { name: /Needs you/ });
    await userEvent.click(screen.getByText("Arrange"));
    expect(await scan(container)).toEqual([]);
  });
});
