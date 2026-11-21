import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";
import { releaseFixtures } from "../test/releasesFixtures";

const open = async (path: string) => {
  history.replaceState(null, "", path);
  render(<App />);
  await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
};

describe("releases", () => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; installServer(releaseFixtures(), me({ roles: { "shop-api": "admin" } })); });
  afterEach(() => vi.unstubAllGlobals());

  it("lists releases with their state and offers to make one", async () => {
    await open("/deployments/releases");
    const table = await screen.findByRole("table", {}, { timeout: 8000 });
    expect(within(table).getByText("Withdrawn")).toBeTruthy();
    expect(within(table).getByText("Candidate")).toBeTruthy();
    expect(screen.getByRole("button", { name: /New release/ })).toBeTruthy();
  }, 20000);

  it("shows a published release as fixed: no editing, its Cargo with checksums and where each came from", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    await screen.findByText(/never change/, {}, { timeout: 8000 });
    expect(screen.queryByRole("button", { name: /Edit the notes/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Publish$/ })).toBeNull();
    expect(screen.getByRole("link", { name: "shop-api-linux-amd64" })).toBeTruthy();
    expect(screen.getAllByText(/Sortie #7 at 01234567, added by ann/).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: /^Withdraw$/ })).toBeTruthy();
  }, 20000);

  it("lets a draft be edited and published", async () => {
    await open("/deployments/releases/shop-api/1.5.0");
    expect(await screen.findByRole("button", { name: /Edit the notes/ }, { timeout: 8000 })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Publish$/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Make it a candidate/ })).toBeTruthy();
  }, 20000);

  it("offers each Airfield's promotion only where the server said it may, ", async () => {
    await open("/deployments/releases/matrix");
    expect((await screen.findAllByRole("button", { name: /Promote 1\.4\.0/ }, { timeout: 8000 })).length).toBe(1);
    expect(screen.getAllByRole("button", { name: /Promote 5\.1\.0/ }).length).toBeGreaterThan(0);
  }, 20000);
});
