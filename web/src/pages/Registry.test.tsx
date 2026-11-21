import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";

function open(path: string) {
  history.replaceState(null, "", path);
  render(<App />);
}

describe("the registry page", () => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; });
  afterEach(() => vi.unstubAllGlobals());

  it("tells how to log in and push, and plainly that blobs are not encrypted by Launchway", async () => {
    installServer();
    open("/images/registry");
    expect(await screen.findByText(/docker push registry.example.com/, {}, { timeout: 8000 })).toBeTruthy();
    expect(screen.getByText("Not encrypted by Launchway")).toBeTruthy();
    expect(screen.getByRole("link", { name: "shop-api" }).getAttribute("href")).toBe("/images/registry/shop-api");
  });

  it("lists a repository's images with what is known of each, and a pull reference to copy", async () => {
    installServer({}, me({ admin: false, roles: { "shop-api": "operator" } }));
    open("/images/registry/shop-api?repository=shop-api/web");
    const table = await screen.findByRole("table", { name: "Images in shop-api/web" }, { timeout: 8000 });
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(4);
    expect(within(rows[1]).getByText("Signed")).toBeTruthy();
    expect(within(rows[1]).getByText(/2 high, 5 medium, 9 low/)).toBeTruthy();
    expect(within(rows[2]).getByText("linux/amd64, linux/arm64")).toBeTruthy();
    expect(within(rows[3]).getByText("untagged")).toBeTruthy();
    expect(within(rows[1]).getByRole("button", { name: "Copy the pull reference of 1.4.0" })).toBeTruthy();
    // A viewer or operator is not offered Delete (the server refuses anyone but a project admin).
    expect(within(table).queryByRole("button", { name: /^Delete/ })).toBeNull();
  });

  it("offers Delete to a project admin, asks first, and then deletes", async () => {
    installServer({}, me({ admin: false, roles: { "shop-api": "admin" } }));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    open("/images/registry/shop-api?repository=shop-api/web");
    const del = await screen.findByRole("button", { name: "Delete 1.4.0" }, { timeout: 8000 });
    const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    fireEvent.click(del);
    expect(confirm).toHaveBeenCalled();
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, init]) => String(u).includes("/registry/image?repository=shop-api%2Fweb&reference=sha256%3Aaaaa") && (init as RequestInit)?.method === "DELETE")).toBe(true));
  });

  it("does not delete when the person says no", async () => {
    installServer({}, me({ admin: false, roles: { "shop-api": "admin" } }));
    vi.spyOn(window, "confirm").mockReturnValue(false);
    open("/images/registry/shop-api?repository=shop-api/web");
    fireEvent.click(await screen.findByRole("button", { name: "Delete 1.4.0" }, { timeout: 8000 }));
    const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit)?.method === "DELETE")).toBe(false);
  });

  it("previews what retention would delete before anything is deleted", async () => {
    installServer({}, me({ admin: false, roles: { "shop-api": "operator" } }));
    open("/images/registry/shop-api");
    fireEvent.click(await screen.findByRole("button", { name: "Preview what retention would delete" }, { timeout: 8000 }));
    expect(await screen.findByText(/untagged and older than 7 days/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Delete these/ })).toBeNull();
    expect(screen.getByText(/needs the admin role on shop-api/)).toBeTruthy();
  });

  it("says plainly when the registry is off", async () => {
    installServer({}, me());
    const real = globalThis.fetch;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).startsWith("/api/v1/registry")) return new Response(JSON.stringify({ error: "off", code: "feature_off" }), { status: 404, headers: { "Content-Type": "application/json" } });
      return real(input, init);
    }));
    open("/images/registry");
    expect(await screen.findByText("The built-in Depot is off", {}, { timeout: 8000 })).toBeTruthy();
  });
});
