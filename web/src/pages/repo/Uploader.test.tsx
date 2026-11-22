import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Uploader } from "./Uploader";

const calls: string[] = [];
beforeEach(() => {
  calls.length = 0;
  history.replaceState(null, "", "/x");
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${new URL(url, "http://x").pathname} ${init?.body ?? ""}`);
    return new Response(JSON.stringify({ branch: "main", paths: ["docs/a.txt"], commit: "c", parent: "p", name: "x", commit2: "" }), { status: 201, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const pick = (files: File[]) => fireEvent.change(screen.getByLabelText(/^Files/), { target: { files } });
const file = (name: string, text: string) => new File([text], name, { type: "text/plain" });

describe("uploading files", () => {
  it("makes one commit of the chosen files under a folder", async () => {
    render(<Uploader repo="acme/shop" refName="main" protectedNames={[]} dir="docs" />);
    expect((screen.getByRole("button", { name: /^Commit/ }) as HTMLButtonElement).disabled).toBe(true);
    pick([file("a.txt", "A"), file("b.txt", "B")]);
    expect(await screen.findByText("docs/a.txt")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Commit 2 files" }));
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST /api/v1/repositories/acme/shop/files"))).toBe(true));
    const body = JSON.parse(calls.find((c) => c.startsWith("POST"))!.split(" ").slice(2).join(" "));
    expect(body).toMatchObject({ branch: "main", message: "Add 2 files", replace: false });
    expect(body.files.map((f: { path: string; content: string }) => [f.path, f.content])).toEqual([["docs/a.txt", "QQ=="], ["docs/b.txt", "Qg=="]]);
  });
  it("refuses a file that is too large before sending anything, and says what to do", () => {
    render(<Uploader repo="acme/shop" refName="main" protectedNames={[]} dir="" />);
    pick([new File([new Uint8Array(2 << 20)], "big.bin")]);
    expect(screen.getByRole("alert").textContent).toMatch(/big\.bin is 2\.0 MiB.*Push larger files with git/);
    expect((screen.getByRole("button", { name: /^Commit/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(calls).toHaveLength(0);
  });
  it("on a protected branch goes to a new branch with a pull request, and says so", async () => {
    render(<Uploader repo="acme/shop" refName="main" protectedNames={["main"]} dir="" />);
    expect(screen.getByText(/a protected branch: only its administrators may/)).toBeTruthy();
    pick([file("a.txt", "A")]);
    fireEvent.click(await screen.findByRole("button", { name: "Commit and open a pull request" }));
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST /api/v1/repositories/acme/shop/branches "))).toBe(true));
    await waitFor(() => expect(calls.some((c) => c.startsWith("POST /api/v1/repositories/acme/shop/files"))).toBe(true));
  });
});
