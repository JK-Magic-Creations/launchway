import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { agent, agentDetail, board, burndown, detail, item, settings, sprint, workAnswers } from "../test/workFixtures";
import { serve, type Call } from "../test/workFixtures";
import Agents from "./Agents";
import Backlog from "./Backlog";
import Board from "./Board";
import Inbox from "./Inbox";
import ItemList from "./ItemList";
import ItemPage from "./ItemPage";
import { SprintPage } from "./Sprints";
import WorkSettings from "./WorkSettings";

const B = "/api/v1/work";
const A = workAnswers();
const get = (path: string) => ({ ["GET " + B + path]: A[B + path] });
const SCOPE = "project:acme/shop";
const last = (calls: Call[], method: string, path: string) => calls.filter((c) => c.method === method && c.url.startsWith(path)).at(-1);

afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, "", "/"); });

describe("the item list", () => {
  it("lists items and runs what is typed in the query box through the server", async () => {
    const calls = serve({ ...get("/items"), ...get("/views") });
    render(<ItemList scope={SCOPE} />);
    expect(await screen.findByText("Round prices to the cent")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Ship the receipt" })).toHaveAttribute("href", "/work/item/PAY-2");
    expect(screen.getByText("agent")).toBeTruthy();
    fireEvent.change(screen.getByRole("searchbox", { name: "Query" }), { target: { value: "type:bug label:ui" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(calls.some((c) => c.url.includes("q=type%3Abug+label%3Aui"))).toBe(true));
  });
  it("turns a filter into a query term and offers saved views", async () => {
    const calls = serve({ ...get("/items"), ...get("/views") });
    render(<ItemList scope={SCOPE} />);
    await screen.findByText("Round prices to the cent");
    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "urgent" } });
    await waitFor(() => expect(calls.some((c) => c.url.includes("priority%3Aurgent"))).toBe(true));
    expect(screen.getByRole("option", { name: "My bugs" })).toBeTruthy();
  });
  it("says what to do when nothing matches", async () => {
    serve({ ["GET " + B + "/items"]: { items: [], total: 0, limit: 25, offset: 0 }, ...get("/views") });
    render(<ItemList scope={SCOPE} />);
    expect(await screen.findByText("No open items yet")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "New item" }).length).toBeGreaterThan(0);
  });
  it("creates an item from the form", async () => {
    const calls = serve({ ...get("/items"), ...get("/views"), ["POST " + B + "/items"]: item({ ref: "PAY-8" }), ["GET " + B + "/items/PAY-8"]: detail() });
    render(<ItemList scope={SCOPE} />);
    await screen.findByText("Round prices to the cent");
    fireEvent.click(screen.getAllByRole("button", { name: "New item" })[0]!);
    const form = screen.getByRole("form", { name: "New item" });
    fireEvent.change(within(form).getByLabelText("Title"), { target: { value: "Fix the cart" } });
    fireEvent.change(within(form).getByLabelText("Labels"), { target: { value: "ui, cart" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create item" }));
    await waitFor(() => expect(last(calls, "POST", B + "/items")).toBeTruthy());
    expect(last(calls, "POST", B + "/items")!.body).toMatchObject({ title: "Fix the cart", labels: ["ui", "cart"], scope: { kind: "project", id: "acme/shop" } });
  });
});

describe("the item page", () => {
  const table = { ...get("/items/PAY-1/comments"), ...get("/items/PAY-1/history") };
  it("shows the item, its delivery status, links, code and history with agent-made events marked", async () => {
    serve({ ["GET " + B + "/items/PAY-1"]: detail({ body: "Hello <img src=x onerror=alert(1)> **bold**" }), ...table });
    const { container } = render(<ItemPage itemRef="PAY-1" />);
    expect(await screen.findByText("Merged, on dev since 10:15, not yet on prod.")).toBeTruthy();
    expect(screen.getByText(/Running 1.4.2 since/)).toBeTruthy();
    expect(screen.getByText("Not here yet")).toBeTruthy();
    expect(screen.getByRole("link", { name: "PAY-2" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "acme/shop#7" })).toBeTruthy();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".md strong")?.textContent).toBe("bold");
    expect(screen.getByRole("link", { name: "receipt.png" })).toHaveAttribute("href", "/api/v1/work/attachments/a1");
    const history = await screen.findByText(/moved this from ready to in progress/);
    expect(within(history.closest("li")!).getByText("agent")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Take over" })).toBeNull();
  });
  it("ticks an acceptance criterion with the version it read", async () => {
    const calls = serve({ ["GET " + B + "/items/PAY-1"]: detail(), ["PATCH " + B + "/items/PAY-1"]: item(), ...table });
    render(<ItemPage itemRef="PAY-1" />);
    fireEvent.click(await screen.findByRole("checkbox", { name: "Cart shows two decimals" }));
    await waitFor(() => expect(last(calls, "PATCH", B + "/items/PAY-1")).toBeTruthy());
    expect(last(calls, "PATCH", B + "/items/PAY-1")!.body).toMatchObject({ expected_version: 4, criteria: [{ id: "c1", done: true }, { id: "c2", done: true }] });
  });
  it("tells who holds it, what it cost, and lets a person take over", async () => {
    const calls = serve({ ["GET " + B + "/items/PAY-9"]: agentDetail(), ["POST " + B + "/items/PAY-9/take-over"]: item(), ...get("/items/PAY-1/comments"), ["GET " + B + "/items/PAY-9/comments"]: { comments: [] }, ["GET " + B + "/items/PAY-9/history"]: { item: "PAY-9", events: [] } });
    render(<ItemPage itemRef="PAY-9" />);
    expect(await screen.findByText(/Being worked by agent docs-agent/)).toBeTruthy();
    expect(screen.getByText(/lease until/)).toBeTruthy();
    expect(screen.getByText("$2.45")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Take over" }));
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-9/take-over")).toBeTruthy());
  });
  it("shows the server's words when the item is missing", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "no such item" }), { status: 404 })));
    render(<ItemPage itemRef="PAY-404" />);
    expect(await screen.findByText("no such item")).toBeTruthy();
    expect(screen.getByText(/may have been removed/)).toBeTruthy();
  });
});

describe("the board", () => {
  it("moves a card with the keyboard alone and says so", async () => {
    const user = userEvent.setup();
    const calls = serve({ ...get("/board"), ...get("/sprints"), ["POST " + B + "/items/PAY-2/move"]: item() });
    render(<Board scope={SCOPE} />);
    const open = await screen.findByRole("button", { name: "Move to… (PAY-2)" });
    expect(screen.getByText("Over the limit of 1")).toBeTruthy();
    open.focus();
    await user.keyboard("{Enter}");
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Ready (here)" })).toBeDisabled();
    // The first choice that can be chosen has the focus: In progress.
    expect(within(menu).getByRole("menuitem", { name: "In progress" })).toHaveFocus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-2/move")).toBeTruthy());
    expect(last(calls, "POST", B + "/items/PAY-2/move")!.body).toEqual({ state: "in_progress", before: "" });
    await waitFor(() => expect(screen.getByTestId("board-live")).toHaveTextContent("Moved PAY-2 to In progress."));
    expect(screen.getByTestId("board-live")).toHaveAttribute("aria-live", "polite");
  });
  it("reorders inside a column with Move up and closes the menu with Escape", async () => {
    const user = userEvent.setup();
    const calls = serve({ ...get("/board"), ...get("/sprints"), ["POST " + B + "/items/PAY-3/rank"]: item() });
    render(<Board scope={SCOPE} />);
    const open = await screen.findByRole("button", { name: "Move to… (PAY-3)" });
    await user.click(open);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(open).toHaveFocus();
    await user.click(open);
    await user.click(screen.getByRole("menuitem", { name: "Move up" }));
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-3/rank")).toBeTruthy());
    expect(last(calls, "POST", B + "/items/PAY-3/rank")!.body).toEqual({ where: "before", other: "PAY-1" });
  });
  it("moves a card dropped on a column", async () => {
    const calls = serve({ ...get("/board"), ...get("/sprints"), ["POST " + B + "/items/PAY-2/move"]: item() });
    render(<Board scope={SCOPE} />);
    await screen.findByRole("button", { name: "Move to… (PAY-2)" });
    const col = screen.getByRole("group", { name: "Done, 0" });
    fireEvent.drop(col, { dataTransfer: { getData: () => "PAY-2" } });
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-2/move")?.body).toEqual({ state: "done", before: "" }));
  });
  it("shows an empty board with a next step", async () => {
    serve({ ["GET " + B + "/board"]: { scope: board().scope, columns: board().columns.map((c) => ({ ...c, items: [], count: 0 })) }, ...get("/sprints") });
    render(<Board scope={SCOPE} />);
    expect(await screen.findByText("The board is empty")).toBeTruthy();
  });
});

describe("the backlog", () => {
  it("moves an item with the menu and says where it went", async () => {
    const user = userEvent.setup();
    const calls = serve({ ...get("/items"), ...get("/sprints"), ["POST " + B + "/items/PAY-2/rank"]: item() });
    render(<Backlog scope={SCOPE} />);
    await user.click(await screen.findByRole("button", { name: "Move… (PAY-2)" }));
    await user.click(screen.getByRole("menuitem", { name: "Move to top" }));
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-2/rank")?.body).toEqual({ where: "top", other: "" }));
    await waitFor(() => expect(screen.getByTestId("backlog-live")).toHaveTextContent("PAY-2 moved to the top of the backlog."));
  });
  it("moves an item before another one chosen from a list", async () => {
    const user = userEvent.setup();
    const calls = serve({ ...get("/items"), ...get("/sprints"), ["POST " + B + "/items/PAY-2/rank"]: item() });
    render(<Backlog scope={SCOPE} />);
    await user.click(await screen.findByRole("button", { name: "Move… (PAY-2)" }));
    await user.click(screen.getByRole("menuitem", { name: "Move before…" }));
    expect(await screen.findByLabelText("Move PAY-2 before")).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Move" }));
    await waitFor(() => expect(last(calls, "POST", B + "/items/PAY-2/rank")?.body).toEqual({ where: "before", other: "PAY-1" }));
  });
  it("puts an item in a sprint with a select", async () => {
    const calls = serve({ ...get("/items"), ...get("/sprints"), ["PATCH " + B + "/items/PAY-1"]: item() });
    render(<Backlog scope={SCOPE} />);
    fireEvent.change(await screen.findByLabelText("Sprint for PAY-1"), { target: { value: "Sprint 5" } });
    await waitFor(() => expect(last(calls, "PATCH", B + "/items/PAY-1")?.body).toEqual({ sprint: "Sprint 5" }));
  });
});

describe("a sprint", () => {
  it("shows commitment, capacity, the advice and the burndown as a chart and a table", async () => {
    serve({ ["GET " + B + "/sprints/sp_1"]: sprint(), ["GET " + B + "/sprints/sp_1/burndown"]: burndown() });
    render(<SprintPage scope={SCOPE} id="sp_1" />);
    expect(await screen.findByText(/21 points are committed against 13 points of capacity/)).toBeTruthy();
    const commit = screen.getByRole("table", { name: "Commitment and completion of Sprint 4" });
    expect(within(commit).getByRole("row", { name: /Committed 21/ })).toBeTruthy();
    expect(within(commit).getByRole("row", { name: /Completed 13/ })).toBeTruthy();
    const cap = screen.getByRole("table", { name: "Capacity of each person in Sprint 4" });
    expect(within(cap).getByRole("row", { name: /ada 8/ })).toBeTruthy();
    expect(screen.getByRole("img", { name: "Burndown of Sprint 4" })).toBeTruthy();
    const table = await screen.findByRole("table", { name: /Burndown of Sprint 4, in points/ });
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(within(table).getByRole("row", { name: /2026-11-11 15 13 6/ })).toBeTruthy();
  });
});

describe("agents", () => {
  const registered = { agent: agent({ name: "new-agent" }), token: "lw_agent_ONLY_ONCE", note: "shown once" };
  it("shows a new token once, with a warning and a copy button, and never again", async () => {
    const user = userEvent.setup();
    const answers: Record<string, unknown> = { ...get("/agents"), ...get("/spend"), "GET /api/v1/repositories": { items: [] }, ["POST " + B + "/agents"]: registered };
    const calls = serve(answers);
    render(<Agents />);
    await screen.findByRole("table", { name: "Registered agents" });
    await user.click(screen.getAllByRole("button", { name: "Register an agent" })[0]!);
    await user.type(screen.getByLabelText("Name"), "new-agent");
    await user.click(screen.getByRole("button", { name: "Register and show token" }));
    const box = await screen.findByRole("alert", { name: "New agent token" });
    expect(within(box).getByText(/only time the token is shown/)).toBeTruthy();
    expect(within(box).getByLabelText("Token")).toHaveValue("lw_agent_ONLY_ONCE");
    await user.click(within(box).getByRole("button", { name: "Copy token" }));
    expect(await navigator.clipboard.readText()).toBe("lw_agent_ONLY_ONCE");
    expect(last(calls, "POST", B + "/agents")!.body).toMatchObject({ name: "new-agent", scope: { kind: "installation" } });
    await user.click(screen.getByRole("button", { name: "I have stored it" }));
    expect(screen.queryByDisplayValue("lw_agent_ONLY_ONCE")).toBeNull();
    expect(document.body.textContent).not.toContain("lw_agent_ONLY_ONCE");
  });
  it("lists state, queue, budget against use, and asks before switching an agent off", async () => {
    const user = userEvent.setup();
    const calls = serve({ ...get("/agents"), ...get("/spend"), ...get("/agents/docs-agent/actions"), "GET /api/v1/repositories": { items: [] }, ["PATCH " + B + "/agents/docs-agent"]: agent({ disabled: true }) });
    render(<Agents />);
    const agents = within(await screen.findByRole("table", { name: "Registered agents" }));
    const row = agents.getByRole("button", { name: "docs-agent" }).closest("tr")!;
    expect(within(row).getByText("On")).toBeTruthy();
    expect(within(row).getByText("$2.45 of $10.00")).toBeTruthy();
    expect(within(agents.getByRole("button", { name: "review-agent" }).closest("tr")!).getByText("Switched off")).toBeTruthy();
    expect(screen.getByRole("table", { name: "Spend by agent, project and item" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "docs-agent" }));
    await user.click(await screen.findByRole("button", { name: "Switch off" }));
    expect(last(calls, "PATCH", B + "/agents/docs-agent")).toBeUndefined();
    await user.click(screen.getByRole("button", { name: "Yes, switch off" }));
    await waitFor(() => expect(last(calls, "PATCH", B + "/agents/docs-agent")?.body).toEqual({ disabled: true }));
  });
});

describe("settings", () => {
  const put = (calls: Call[]) => last(calls, "PUT", B + "/settings");
  it("shows the autonomy policy: levels 3 and 4 are off and say so", async () => {
    serve({ ["GET " + B + "/settings"]: settings() });
    render(<WorkSettings scope={SCOPE} />);
    const group = await screen.findByRole("group", { name: /How far may an agent's work go/ });
    expect(within(group).getByRole("radio", { name: /Level 3/ })).toBeDisabled();
    expect(within(group).getByRole("radio", { name: /Level 4/ })).toBeDisabled();
    expect(within(group).getAllByText("Not yet available")).toHaveLength(2);
    expect(within(group).getByRole("radio", { name: /Level 1/ })).toBeChecked();
    expect(screen.getByText(/The default: no policy has been set/)).toBeTruthy();
    expect(screen.queryByLabelText(/Reason for raising the level/)).toBeNull();
  });
  it("needs a reason above level 1 and sends it with the level", async () => {
    const user = userEvent.setup();
    const calls = serve({ ["GET " + B + "/settings"]: settings(), ["PUT " + B + "/settings"]: settings() });
    render(<WorkSettings scope={SCOPE} />);
    await user.click(await screen.findByRole("radio", { name: /Level 2/ }));
    const reason = screen.getByLabelText(/Reason for raising the level/);
    expect(reason).toBeRequired();
    await user.type(screen.getByLabelText(/Paths an agent may change/), "docs/**");
    fireEvent.submit(reason.closest("form")!);
    expect(await screen.findByText(/Say why the autonomy level is raised/)).toBeTruthy();
    expect(put(calls)).toBeUndefined();
    await user.type(reason, "Docs only, reviewed weekly");
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(put(calls)).toBeTruthy());
    expect(put(calls)!.body).toMatchObject({ scope: SCOPE, settings: { agents: { autonomy: { level: 2, paths: ["docs/**"], reason: "Docs only, reviewed weekly" } } } });
  });
  it("shows where an inherited policy comes from, and locks the form for a reader", async () => {
    serve({ ["GET " + B + "/settings"]: settings({ can_change: false, autonomy: { ...settings().autonomy, level: 2, source: "installation" } }) });
    render(<WorkSettings scope={SCOPE} />);
    expect(await screen.findByText(/Inherited from Base/)).toBeTruthy();
    expect(screen.getByText(/You can read these settings but not change them/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
  });
  it("makes the agent review policy ask for paths and a reason", async () => {
    const user = userEvent.setup();
    serve({ ["GET " + B + "/settings"]: settings() });
    render(<WorkSettings scope={SCOPE} />);
    await user.click(await screen.findByRole("radio", { name: /An independent agent's approval may count/ }));
    expect(screen.getByLabelText(/Paths it covers/)).toBeRequired();
    expect(screen.getByLabelText(/Why \(required, recorded\)/)).toBeRequired();
  });
});

describe("the inbox", () => {
  it("says email is not available and marks everything read", async () => {
    const calls = serve({ ...get("/inbox"), ["POST " + B + "/inbox/read"]: { ok: true } });
    render(<Inbox />);
    expect(await screen.findByText(/Email is not available/)).toBeTruthy();
    expect(screen.getByText("PAY-1 was assigned to you")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Mark all read" }));
    await waitFor(() => expect(last(calls, "POST", B + "/inbox/read")?.body).toEqual({ ids: [1] }));
  });
});
