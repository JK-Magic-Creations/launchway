import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

// The rollback views that need a click to appear (the "Roll back to…" picker, the confirmation that states the
// consequence and asks for a reason, and the review dialog), scanned by axe-core in both themes. The environment
// page with its button, the Needs review list and the rollback run's page are in pages.a11y.test.tsx.
describe.each(["light", "dark"])("rollback views, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async () => {
    history.replaceState(null, "", "/deployments/environments/prod");
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };
  const expectClean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the confirmation with the consequence and the reason", async () => {
    await open();
    fireEvent.click(await screen.findByRole("button", { name: /Roll back to 1\.3\.2/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await expectClean("rollback confirmation");
  }, 20000);
  it("the picker of an older version", async () => {
    await open();
    fireEvent.click(await screen.findByRole("button", { name: /Roll back to…/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await expectClean("rollback picker");
  }, 20000);
  it("the review dialog", async () => {
    await open();
    const list = await screen.findByText(/emergency rollback/, {}, { timeout: 8000 });
    expect(list).toBeTruthy();
    fireEvent.click(await screen.findByRole("button", { name: /Acknowledge/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await expectClean("review dialog");
  }, 20000);
});
