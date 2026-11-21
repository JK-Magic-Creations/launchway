import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

// The toggle views that need a click to appear: the rules editor with a rule and a condition open, the new-toggle
// form, the change-safeguards form and the rollback confirmation with the toggle changes. The pages themselves
// are in pages.a11y.test.tsx. The baseline stays empty: findings are fixed, not listed.
describe.each(["light", "dark"])("toggle views, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  const open = async (path: string) => { history.replaceState(null, "", path); render(<App />); await waitFor(() => screen.getByRole("main"), { timeout: 8000 }); };
  const clean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the rules editor with a rule and a condition", async () => {
    await open("/projects/shop-api/toggles/checkout-v2");
    fireEvent.click((await screen.findAllByRole("button", { name: "Edit" }, { timeout: 8000 }))[0]!);
    fireEvent.click(await screen.findByRole("button", { name: "Add a rule" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Add a condition" })[0]!);
    await clean("the rules editor");
  }, 20000);
  it("the new toggle form", async () => {
    await open("/projects/shop-api/toggles");
    fireEvent.click(await screen.findByRole("button", { name: "New toggle" }, { timeout: 8000 }));
    await screen.findByRole("dialog");
    await clean("the new toggle form");
  }, 20000);
  it("the safeguards form and the key dialog", async () => {
    await open("/projects/shop-api/toggle-settings");
    fireEvent.click(await screen.findByRole("button", { name: "Change the safeguards of prod" }, { timeout: 8000 }));
    await screen.findByRole("dialog");
    await clean("the safeguards form");
  }, 20000);
  it("the rollback confirmation lists the toggle changes", async () => {
    await open("/deployments/environments/prod");
    fireEvent.click(await screen.findByRole("button", { name: /Return to base: 1\.3\.2/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await screen.findByText(/Toggle changes since 1\.3\.2 went live/, {}, { timeout: 8000 });
    await clean("rollback with toggle changes");
  }, 20000);
});
