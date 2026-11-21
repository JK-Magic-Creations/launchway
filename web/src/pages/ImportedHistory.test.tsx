import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import ImportedHistory from "./ImportedHistory";

const row = (n: number) => ({ number: n, title: `Issue ${n}`, state: n % 2 ? "open" : "closed", author: "ada", body: "", labels: [], assignees: [], comment_count: n === 7 ? 1 : 0, created_at: "2024-01-02T03:04:05Z" });
const detail = { ...row(7), body: "Hello <b>bold</b> <img src=x onerror=alert(1)>", labels: ["bug"], comments: [{ id: "1", author: "bob", body: "<script>x</script> thanks", created_at: "2024-01-03T00:00:00Z" }] };

function serve() {
  const urls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    urls.push(url);
    const u = new URL(url, "http://x");
    const body = u.pathname.endsWith("/7") ? detail
      : { items: u.searchParams.get("page") === "2" ? [row(1)] : [row(7), row(6)], total: 3, open: 2, closed: 1, page: Number(u.searchParams.get("page")), per_page: 2 };
    return new Response(JSON.stringify(body), { status: 200 });
  }));
  return urls;
}

afterEach(() => vi.unstubAllGlobals());

describe("the Imported history tab", () => {
  it("lists the issues, pages, searches and filters through the server", async () => {
    const urls = serve();
    render(<ImportedHistory path="acme/shop" />);
    expect(await screen.findByText("Issue 7")).toBeTruthy();
    expect(screen.getByText(/Page 1 of 2, 3 issues/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await waitFor(() => expect(screen.getByText("Issue 1")).toBeTruthy());
    expect((screen.getByRole("button", { name: "Older" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Search titles/), { target: { value: "zebra" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(urls.some((u) => u.includes("q=zebra") && u.includes("page=1"))).toBe(true));
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "closed" } });
    await waitFor(() => expect(urls.some((u) => u.includes("state=closed"))).toBe(true));
  });
  it("shows an issue and its comments as text, never as markup", async () => {
    serve();
    const { container } = render(<ImportedHistory path="acme/shop" />);
    fireEvent.click(await screen.findByRole("button", { name: "Issue 7" }));
    expect(await screen.findByText(/Hello <b>bold<\/b>/)).toBeTruthy();
    expect(screen.getByText(/<script>x<\/script> thanks/)).toBeTruthy();
    expect(container.querySelector("script, b, img")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Back to the list" }));
    expect(await screen.findByText("Issue 6")).toBeTruthy();
  });
  it("says what an empty archive is", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ items: [], total: 0, open: 0, closed: 0, page: 1, per_page: 50 }), { status: 200 })));
    render(<ImportedHistory path="acme/shop" />);
    expect(await screen.findByText("No imported issues")).toBeTruthy();
    expect(screen.getByText(/import-archive/)).toBeTruthy();
  });
});
