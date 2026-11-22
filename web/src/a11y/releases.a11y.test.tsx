import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";
import { releaseFixtures } from "../test/releasesFixtures";
import { scan } from "../test/axe";

// The release pages, drawn whole with fixtures, in both themes, scanned by axe-core with no baseline: the list,
// a published release, a draft, the matrix, the calendar, the formations and the report, and the dialogs that
// need a click (promote with its checklist, make a release, withdraw).
describe.each(["light", "dark"])("releases, %s theme", (theme) => {
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never;
    document.documentElement.setAttribute("data-theme", theme);
    installServer(releaseFixtures(), me({ roles: { "shop-api": "admin", "shop-web": "operator" } }));
  });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async (path: string) => {
    history.replaceState(null, "", path);
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 150));
  };
  const clean = async (what: string) => {
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it.each([
    ["the list", "/deployments/releases"], ["a published release", "/deployments/releases/shop-api/1.4.0"], ["a draft", "/deployments/releases/shop-api/1.5.0"],
    ["the matrix", "/deployments/releases/matrix"], ["the calendar", "/deployments/releases/calendar"], ["the formations", "/deployments/releases/formations"], ["the report", "/deployments/releases/report"],
  ])("%s has no accessibility violations", async (what, path) => {
    await open(path);
    await clean(what);
  }, 20000);

  it("the page says what it is", async () => {
    await open("/deployments/releases/matrix");
    expect(await screen.findByText("Where each release is", { selector: "h1" }, { timeout: 8000 })).toBeTruthy();
    expect(screen.getAllByText(/Return to base/).length).toBeGreaterThan(0);
  }, 20000);

  it("the promote dialog with its checklist", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    fireEvent.click(await screen.findByRole("button", { name: /Check and promote to prod/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await screen.findByText(/Inside the change window/, {}, { timeout: 8000 });
    await clean("promote dialog");
  }, 20000);

  it("the download page a QR code opens", async () => {
    await open("/deployments/releases/shop-api/1.4.0/get/shop-api-linux-amd64");
    await screen.findByText(/The download should start now/, {}, { timeout: 8000 });
    await clean("download page");
  }, 20000);

  it("the dialog that cuts a release", async () => {
    await open("/projects/shop-api");
    fireEvent.click(await screen.findByRole("button", { name: /Cut a release/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await screen.findByText(/Attach the 1 file built at this commit/, {}, { timeout: 8000 });
    await clean("cut a release dialog");
  }, 20000);

  it("the dialog that makes a release", async () => {
    await open("/deployments/releases");
    fireEvent.click(await screen.findByRole("button", { name: /New release/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await clean("new release dialog");
  }, 20000);

  it("the dialog that withdraws a release", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    fireEvent.click(await screen.findByRole("button", { name: /^Withdraw$/ }, { timeout: 8000 }));
    await screen.findByRole("dialog", {}, { timeout: 8000 });
    await clean("withdraw dialog");
  }, 20000);
});
