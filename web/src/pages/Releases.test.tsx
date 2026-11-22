import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";
import { releaseFixtures, releaseWithALongFileName, view } from "../test/releasesFixtures";

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
    expect(within(table).getAllByText("Latest").length).toBe(1); // only the newest published release that is not a pre-release
    expect(within(table).getByText("Pre-release")).toBeTruthy();
  }, 20000);

  it("marks the latest release and counts the downloads of each file", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    await screen.findByText(/never change/, {}, { timeout: 8000 });
    expect(screen.getByText("Latest")).toBeTruthy();
    expect(screen.getByText(/This is the latest release/)).toBeTruthy();
    const row = screen.getByRole("link", { name: "shop-api-linux-amd64" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("42")).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Downloads" })).toBeTruthy();
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

  it("shows how a published release stands on each outside Git host, with the reason and a way to try again", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    const sheet = (await screen.findByRole("heading", { name: "On an outside Git host" }, { timeout: 8000 })).closest("section") as HTMLElement;
    expect(within(sheet).getByText("Published")).toBeTruthy();
    expect(within(sheet).getByRole("link", { name: /releases\/tag\/v1\.4\.0/ })).toBeTruthy();
    expect(within(sheet).getByText(/may not be allowed to make releases/)).toBeTruthy();
    expect(within(sheet).getAllByRole("button", { name: /Retry/ }).length).toBe(1); // only the failed one
  }, 20000);

  it("draws a QR code for a short address and, for an address too long to draw, says so and shows the address to copy", async () => {
    await open("/deployments/releases/shop-api/1.4.0");
    const phone = (await screen.findByRole("heading", { name: "Get it on a phone" }, { timeout: 8000 })).closest("section") as HTMLElement;
    expect(within(phone).getByRole("img", { name: /QR code that downloads shop-api-linux-amd64/ })).toBeTruthy();
    expect(within(phone).queryByText(/too long for a QR code/)).toBeNull();
    vi.unstubAllGlobals();
    installServer({ ...releaseFixtures(), "/api/v1/projects/shop-api/releases/1.4.0": view(releaseWithALongFileName()) }, me({ roles: { "shop-api": "admin" } }));
    document.body.innerHTML = "";
    await open("/deployments/releases/shop-api/1.4.0");
    const long = (await screen.findByRole("heading", { name: "Get it on a phone" }, { timeout: 8000 })).closest("section") as HTMLElement;
    expect(within(long).getByRole("status").textContent).toMatch(/This address is too long for a QR code\. Copy it instead: .*very-long-name/);
    expect(within(long).queryByRole("img", { name: /QR code/ })).toBeNull();
  }, 30000);

  it("draws no outside sheet for a draft", async () => {
    await open("/deployments/releases/shop-api/1.5.0");
    await screen.findByRole("button", { name: /Edit the notes/ }, { timeout: 8000 });
    expect(screen.queryByText("On an outside Git host")).toBeNull();
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
