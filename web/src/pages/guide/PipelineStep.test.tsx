import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { GuideStep } from "../../guideApi";
import { PipelineStep } from "./OtherSteps";

afterEach(cleanup);

// The setup guide's step for a first pipeline leads to Add a Flight, the one way a repository gets a pipeline; it does
// not carry a wizard of its own any more.
const step = (available: boolean): GuideStep => ({
  id: "pipeline", title: "A first pipeline", summary: "Pick a repository.", state: "todo",
  actions: [{ name: "start_wizard", who: "admin", available, reason: available ? "" : "Needs the administrator role" }],
} as unknown as GuideStep);

describe("the setup guide's pipeline step", () => {
  it("links to Add a Flight", () => {
    render(<PipelineStep step={step(true)} />);
    expect(screen.getByRole("link", { name: /Add a (Flight|Codebase)/ })).toHaveAttribute("href", "/projects/new");
  });
  it("gives the reason instead when the person may not", () => {
    render(<PipelineStep step={step(false)} />);
    expect(screen.queryByRole("link", { name: /Add a/ })).toBeNull();
    expect(screen.getByText(/administrator role/)).toBeInTheDocument();
  });
});
