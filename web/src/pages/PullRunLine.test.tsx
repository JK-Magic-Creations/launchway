import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PullRunLine } from "./PullPeople";
import { pullFixtures } from "../test/mockApi";

afterEach(() => vi.unstubAllGlobals());

describe("the latest run on a pull request", () => {
  it("draws the run's launch line with a link to the run", async () => {
    const fx = pullFixtures();
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(fx[url.split("?")[0]]), { status: 200 })));
    render(<PullRunLine runId="run_7" head="abc" />);
    const link = await screen.findByRole("link", { name: /#7 of shop-api/ });
    expect(link.getAttribute("href")).toBe("/runs/run_7");
    expect(screen.getByRole("list", { name: /launch line for run #7/ }).textContent).toContain("Checks");
  });
  it("says so when no run has started", () => {
    render(<PullRunLine runId={undefined} head="abc" />);
    expect(screen.getByText(/No run has started/)).toBeTruthy();
  });
});
