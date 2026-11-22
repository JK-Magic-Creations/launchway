import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { installServer } from "./test/mockApi";

// Walks pages and checks the rule "every view has an address": change the view, read the address, mount the page again from that
// address alone, and expect the same view; an address that names a view opens it directly.
beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; installServer(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const open = async (path: string) => {
  history.replaceState(null, "", path);
  render(<App />);
  await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
};

describe("an address opens the view it names", () => {
  it.each([
    ["/repositories/acme/shop/settings?tab=hooks", () => screen.findByRole("tab", { name: "Webhooks", selected: true }, { timeout: 8000 })],
    ["/projects/shop-api/pipeline?view=history", () => screen.findByText(/Every run of shop-api/, {}, { timeout: 8000 })],
    ["/pipelines?q=shop-web&view=table", async () => { const box = (await screen.findByRole("searchbox", {}, { timeout: 8000 })) as HTMLInputElement; expect(box.value).toBe("shop-web"); return box; }],
    ["/activity?show=needs", () => screen.findByRole("button", { name: "Needs attention", pressed: true }, { timeout: 8000 })],
    ["/activity?status=failed", () => screen.findByRole("button", { name: "Failed", pressed: true }, { timeout: 8000 })],
    ["/activity?by=place", () => screen.findByRole("tree", { name: "Places" }, { timeout: 8000 })],
    ["/projects?q=shop&kind=local", async () => { const box = (await screen.findByRole("searchbox", {}, { timeout: 8000 })) as HTMLInputElement; expect(box.value).toBe("shop"); return box; }],
    ["/runs/run_7?tab=events", () => screen.findByRole("tab", { name: /Event log/, selected: true }, { timeout: 8000 })],
  ])("%s", async (path, expectView) => {
    await open(path);
    expect(await expectView()).toBeTruthy();
    expect(location.pathname + location.search).toBe(path);
  }, 20000);
});

describe("a view that is changed is in the address, and the address restores it", () => {
  it("the Sorties list: choose a filter, read the address, mount again", async () => {
    await open("/activity");
    await userEvent.click(await screen.findByRole("button", { name: "Needs attention" }));
    expect(location.search).toBe("?show=needs");
    await userEvent.click(screen.getByRole("button", { name: "Failed" }));
    expect(location.search).toBe("?show=needs&status=failed");
    const address = location.pathname + location.search;
    cleanup();
    await open(address);
    expect(await screen.findByRole("button", { name: "Needs attention", pressed: true })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Failed", pressed: true })).toBeTruthy();
  }, 20000);
  it("a repository's tab: Back returns to the one before", async () => {
    await open("/repositories/acme/shop/settings");
    await userEvent.click(await screen.findByRole("tab", { name: "Webhooks" }, { timeout: 8000 }));
    expect(location.search).toBe("?tab=hooks");
    expect(document.title).toMatch(/^Hooks · /);
  }, 20000);
  it("a pipeline's view", async () => {
    await open("/projects/shop-api/pipeline");
    await userEvent.click(await screen.findByRole("tab", { name: /History/ }, { timeout: 8000 }));
    expect(location.search).toBe("?view=history");
    expect(document.title).toMatch(/^History · shop-api/);
  }, 20000);
});
