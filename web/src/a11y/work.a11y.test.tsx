import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";
import { workAnswers } from "../test/workFixtures";

// The work pages, drawn whole (shell and page) with fixtures in both themes, and scanned by axe-core. Nothing here is listed in
// baseline.json: these pages have no known findings, and the baseline may only shrink.
const P = "/work/project%3Aacme%2Fshop";
const PAGES = ["/work", `${P}/items`, `${P}/board`, `${P}/backlog`, `${P}/sprints`, `${P}/sprints/sp_1`, `${P}/charts`, `${P}/roadmap`, `${P}/milestones`, `${P}/settings`,
  "/work/item/PAY-1", "/work/item/PAY-9", "/work/agents", "/work/inbox", "/repositories/acme/shop/pulls/1"];

describe.each(["light", "dark"])("work pages, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async (path: string) => {
    installServer({
      ...workAnswers(), "/api/v1/work/items/PAY-9": { ...(workAnswers()["/api/v1/work/items/PAY-1"] as object), ref: "PAY-9", claim: { agent: "docs-agent", claimed_at: "2026-11-21T08:00:00Z", heartbeat_at: "2026-11-21T08:30:00Z", lease_until: "2026-11-21T09:30:00Z" }, usage: { tokens: 1200, cents: 245, seconds: 600, exceeded: false }, actions: ["edit", "comment", "take_over"] },
      "/api/v1/work/items/PAY-9/comments": { comments: [] }, "/api/v1/work/items/PAY-9/history": { events: [] },
    });
    history.replaceState(null, "", path);
    const view = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await waitFor(() => { if (view.container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 200));
  };
  const clean = async (what: string) => {
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it.each(PAGES)("%s has no accessibility violations", async (path) => {
    await open(path);
    await clean(path);
  }, 20000);

  it("the board with a card's Move menu open", async () => {
    await open(`${P}/board`);
    fireEvent.click(await screen.findByRole("button", { name: "Move to… (PAY-2)" }));
    await screen.findByRole("menu");
    await clean("board menu");
  }, 20000);
  it("the backlog with the menu and the before picker open", async () => {
    await open(`${P}/backlog`);
    fireEvent.click(await screen.findByRole("button", { name: "Move… (PAY-2)" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Move before…" }));
    await screen.findByLabelText("Move PAY-2 before");
    await clean("backlog before");
  }, 20000);
  it("the agents page with the register form open", async () => {
    await open("/work/agents");
    fireEvent.click((await screen.findAllByRole("button", { name: "Register an agent" }))[0]!);
    await screen.findByRole("form", { name: "Register an agent" });
    await clean("register form");
  }, 20000);
  it("the autonomy settings with level 2 chosen", async () => {
    await open(`${P}/settings`);
    fireEvent.click(await screen.findByRole("radio", { name: /Level 2/ }));
    await screen.findByLabelText(/Reason for raising the level/);
    await clean("autonomy level 2");
  }, 20000);
});
