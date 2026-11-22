import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installServer } from "../test/mockApi";
import { StepPage } from "./CustomSteps";
import { CustomStepPanel } from "./CustomStepPanel";

describe("custom steps", () => {
  beforeEach(() => installServer());
  afterEach(() => vi.unstubAllGlobals());

  it("the step's page shows what it may do, its versions and the update that asks for more", async () => {
    render(<StepPage library="launchway" name="scan-secrets" />);
    await waitFor(() => screen.getByRole("heading", { name: "What it may do" }));
    expect(screen.getAllByText(/reach no network/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/v2 is newer/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/may now reach evil.example.org/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/shop-api/).length).toBeGreaterThan(0);
  });

  it("a run's record of a custom step shows its permissions, the summary through the renderer and the notes", () => {
    const { container } = render(<CustomStepPanel
      custom={{ name: "notes", library: "platform", commit: "0123456789abcdef", contract: 1, digest: "d", network: ["registry.example.org"], secrets: ["token"] }}
      notes={[{ kind: "summary", text: "**3** files <script>alert(1)</script>" }, { kind: "annotation", level: "warning", file: "a.txt", line: 2, text: "look here" }]} />);
    expect(screen.getByText(/reach registry.example.org/)).toBeTruthy();
    expect(screen.getByText("3").tagName).toBe("STRONG");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/look here/)).toBeTruthy();
  });
});
