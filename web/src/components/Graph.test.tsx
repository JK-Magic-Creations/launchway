import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DIAMOND, FAILED_WITH_SKIPPED, FAN_OUT, LONG_CHAIN, MIXED, MIXED_GROUPS, NOW, OWN_PIPELINE } from "../test/graphFixtures";
import { Graph } from "./Graph";

const draw = (steps = OWN_PIPELINE, extra: Partial<React.ComponentProps<typeof Graph>> = {}) => {
  const onSelect = vi.fn();
  const view = render(<Graph steps={steps} selected={null} onSelect={onSelect} now={NOW} {...extra} />);
  return { onSelect, ...view };
};

describe("the pipeline graph", () => {
  it("is a list of stages, and names the steps that run together", () => {
    draw();
    const stages = screen.getByRole("list", { name: /5 stages, 1 with steps that run together, 2 running now/ });
    expect(within(stages).getAllByRole("listitem", { name: "" }).length).toBeGreaterThan(0);
    expect(screen.getByText("Stage 2 (runs together): test, security-go, security-web")).toBeInTheDocument();
    expect(screen.getByText("Stage 1: check-code")).toBeInTheDocument();
  });

  it("shows every running step as running, in words as well as the picture", () => {
    draw();
    for (const name of ["test", "security-go"]) {
      const node = screen.getByRole("button", { name: new RegExp("^" + name + " Airborne \\(running\\)") });
      expect(node).toHaveClass("t-run");
      expect(node).toHaveTextContent(/Airborne \(running\) · 9m 55s/);
    }
    expect(screen.getByRole("button", { name: /^security-web Passed/ })).not.toHaveClass("t-run");
  });

  it("says what a step follows and what runs beside it", () => {
    draw();
    expect(screen.getByRole("button", { name: /^test Airborne \(running\) · 9m 55s after check-code\. runs together with security-go, security-web/ })).toBeInTheDocument();
  });

  it("chooses a step by click and by keyboard, as before", () => {
    const { onSelect } = draw();
    fireEvent.click(screen.getByRole("button", { name: /^test / }));
    expect(onSelect).toHaveBeenCalledWith("test");
  });

  it("moves between nodes with the arrow keys, following the connectors", () => {
    draw();
    const at = (n: string) => screen.getByRole("button", { name: new RegExp("^" + n + " ") });
    at("check-code").focus();
    fireEvent.keyDown(at("check-code"), { key: "ArrowRight" });
    expect(at("test")).toHaveFocus();
    fireEvent.keyDown(at("test"), { key: "ArrowDown" });
    expect(document.activeElement?.getAttribute("data-node")).toMatch(/security-go|security-web/);
    const second = document.activeElement as HTMLElement;
    fireEvent.keyDown(second, { key: "ArrowLeft" });
    expect(at("check-code")).toHaveFocus();
    fireEvent.keyDown(at("check-code"), { key: "End" });
    expect(at("deploy-dev")).toHaveFocus();
  });

  it("keeps one node in the tab order, the chosen one", () => {
    draw(OWN_PIPELINE, { selected: "image" });
    const tabbable = screen.getAllByRole("button").filter((b) => b.getAttribute("data-node") && b.tabIndex === 0);
    expect(tabbable.map((b) => b.getAttribute("data-node"))).toEqual(["image"]);
  });

  it("draws a wide fan-out, a diamond and skipped dependents with distinct states", () => {
    draw(FAN_OUT);
    expect(screen.getByText("Stage 2 (runs together): shard-1, shard-2, shard-3, shard-4, shard-5, shard-6, shard-7, shard-8")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^shard-8 Waiting for a Station/ })).toBeInTheDocument();
    draw(DIAMOND);
    expect(screen.getByText("Stage 2 (runs together): unit, lint")).toBeInTheDocument();
    draw(FAILED_WITH_SKIPPED);
    expect(screen.getByRole("button", { name: /^test Failed/ })).toHaveClass("t-bad");
    const skipped = screen.getByRole("button", { name: /^build-ui Skipped: not needed/ });
    expect(skipped).toHaveClass("pg-skipped");
  });

  it("marks a step waiting for a person apart from one not started", () => {
    draw(MIXED, { groups: MIXED_GROUPS });
    expect(screen.getByRole("button", { name: /^approve Waiting.*approval step/ })).toHaveClass("t-wait");
    expect(screen.getByRole("button", { name: /^ship Not started/ })).toHaveClass("pg-pending");
  });

  it("folds a template group into one node and opens it again", () => {
    draw(MIXED, { groups: MIXED_GROUPS });
    expect(screen.getByRole("button", { name: /^compile / })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /build · template go\/build/ }));
    const folded = screen.getByRole("button", { name: /^build Airborne \(running\) · 1\/2/ });
    expect(folded).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("button", { name: /^compile / })).toBeNull();
    fireEvent.click(folded);
    expect(screen.getByRole("button", { name: /^compile / })).toBeInTheDocument();
  });

  it("folds the inside of a long chain while it is quiet, and shows it again on request", () => {
    draw([...LONG_CHAIN.slice(0, 5).map((s) => ({ ...s, status: "succeeded" as const })), ...LONG_CHAIN.slice(5, 9), ...[10, 11, 12, 13, 14, 15].map((i) => ({ ...LONG_CHAIN[8], id: "stage-" + i, needs: ["stage-" + (i - 1)] }))].map((s, i, all) => ({ ...s, needs: i ? [all[i - 1].id] : [] })));
    expect(screen.getByRole("button", { name: /^13 in a row / })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /13 steps in a row · stage-2 to stage-14/ }));
    expect(screen.getByRole("button", { name: /^stage-7 / })).toBeInTheDocument();
  });
});
