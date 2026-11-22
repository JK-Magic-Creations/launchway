import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../../App";
import { installServer, squadronFixtures } from "../../test/mockApi";
import { me } from "../../test/fixtures";
import { scan } from "../../test/axe";

// The import wizard over the mock server: paste a file, read the proposal beside the old text with each finding
// anchored to its line, fix a gap, and the checks of the accessibility rules.
const plan = {
  format: "hosted-workflows", files: [".github/workflows/ci.yml"], name: "shop", dry: true, secrets: ["TOKEN"],
  pipeline: "pipeline: shop\nsteps:\n  test:\n    image: golang:1.22\n    run: go test ./...\n",
  steps: [{ job: "test", step: "test", line: 3, template: "go-test" }],
  findings: [
    { code: "hosted.job", class: "exact", message: "The job became a step.", next: "", file: ".github/workflows/ci.yml", line: 2 },
    { code: "hosted.on.pr", class: "different", message: "Pull requests run at their head commit.", next: "Nothing.", file: ".github/workflows/ci.yml", line: 4 },
    { code: "hosted.uses.third", class: "manual", message: "This step runs a third-party program (x/y@v1).", next: "Replace it with the command it runs.", file: ".github/workflows/ci.yml", line: 6, step: "test" },
  ],
};

let calls: { method: string; path: string; body?: Record<string, unknown> }[] = [];
function serve() {
  installServer({ ...squadronFixtures() }, me({ admin: true }));
  const base = globalThis.fetch;
  calls = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, path, body });
    if (path === "/api/v1/import/pipeline") return new Response(JSON.stringify({ plans: [{ ...plan, dry: !!body?.dry }], detected: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
    return base(input, init);
  }));
}

beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; });
afterEach(() => { vi.unstubAllGlobals(); });

describe("Import an existing pipeline", () => {
  it("reads pasted text, shows old and new side by side with findings anchored to lines, and passes the accessibility rules", async () => {
    serve();
    history.replaceState(null, "", "/projects/shop-api/import");
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("heading", { name: /Import an existing pipeline/ }), { timeout: 8000 });
    await userEvent.type(screen.getByLabelText(/The file's text/), "on: push");
    await userEvent.click(screen.getByRole("button", { name: /Read the files/ }));
    await waitFor(() => screen.getByText(/need a person/), { timeout: 8000 });
    expect(calls.find((c) => c.path === "/api/v1/import/pipeline")?.body).toMatchObject({ dry: true, files: [{ content: "on: push" }] });
    const findings = screen.getByRole("list", { name: "Findings" });
    expect(within(findings).getByText(/third-party program/)).toBeInTheDocument();
    // The finding says what to do next and points at its line in the old file.
    expect(within(findings).getByText(/Replace it with the command it runs/)).toBeInTheDocument();
    await userEvent.click(within(findings).getByRole("button", { name: "Show line 6" }));
    // The dry switch asks again without the delivering steps enabled.
    await userEvent.click(screen.getByLabelText(/Run beside the old system/));
    await waitFor(() => expect(calls.filter((c) => c.path === "/api/v1/import/pipeline").length).toBeGreaterThan(1));
    expect(calls.filter((c) => c.path === "/api/v1/import/pipeline").at(-1)?.body).toMatchObject({ dry: false });
    // A secret is named, never given a value.
    expect(screen.getByLabelText(/Secret TOKEN/)).toBeInTheDocument();
    const found = await scan(container.querySelector("main") ?? container, { page: false });
    expect(found).toEqual([]);
  });
});
