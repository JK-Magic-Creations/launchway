import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

// The views of the Git hosting pages that need a click to appear (a repository's deploy keys, protected branches,
// webhooks and a webhook's delivery log, and the owner's webhooks), scanned by axe-core in both themes. The pages
// themselves are in pages.a11y.test.tsx.
describe.each(["light", "dark"])("Git hosting views, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async (path: string) => {
    history.replaceState(null, "", path);
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };
  const expectClean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the SSH clone address", async () => {
    await open("/repositories/acme/shop/settings");
    fireEvent.click(await screen.findByRole("tab", { name: "SSH" }, { timeout: 8000 }));
    await expectClean("SSH clone address");
  }, 20000);
  it.each([["Deploy keys"], ["Webhooks"], ["Protected branches"]])("the %s tab", async (tab) => {
    await open("/repositories/acme/shop/settings");
    fireEvent.click(await screen.findByRole("tab", { name: tab }, { timeout: 8000 }));
    await screen.findByRole("tabpanel", {}, { timeout: 100 }).catch(() => {});
    await expectClean(tab);
  }, 20000);
  it("the delivery log", async () => {
    await open("/repositories/acme/shop/settings?tab=hooks");
    fireEvent.click(await screen.findByRole("button", { name: /Delivery log/ }, { timeout: 8000 }));
    await screen.findByRole("table", { name: "Delivery log" }, { timeout: 8000 });
    await expectClean("delivery log");
  }, 20000);
  it("the owner's webhooks", async () => {
    await open("/owners/acme");
    await screen.findByText(/build server/, {}, { timeout: 8000 });
    await expectClean("owner webhooks");
  }, 20000);
  it("an imported issue", async () => {
    await open("/repositories/acme/shop/history");
    fireEvent.click(await screen.findByRole("button", { name: "Checkout crashes on empty carts" }, { timeout: 8000 }));
    await screen.findByText(/Fixed in the next release/, {}, { timeout: 8000 });
    await expectClean("imported issue");
  }, 20000);
  it("the host key fingerprints", async () => {
    await open("/settings/access/ssh-keys");
    await screen.findByText(/SHA256:hostkeyfingerprint/, {}, { timeout: 8000 });
    await expectClean("host keys");
  }, 20000);
});
