import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Icon, isIcon } from "./Icon";
import { ICON_PATHS } from "./iconPaths";
import { LaunchLine, toStations } from "./LaunchLine";
import { Band, Button, Decision, EmptyState, EvidenceList, PageHead } from "./layout";
import { Chip, Status, StateChip, stateOf, stateOfStation } from "./Status";
import { KitPage } from "../pages/Kit";

describe("icons", () => {
  it("has the whole set, each a drawing on a 24 px grid", () => {
    expect(Object.keys(ICON_PATHS).length).toBeGreaterThanOrEqual(60);
    for (const [name, svg] of Object.entries(ICON_PATHS)) expect(svg.length, name).toBeGreaterThan(10);
    expect(isIcon("home")).toBe(true);
    expect(isIcon("nope")).toBe(false);
  });
  it("is hidden from screen readers", () => {
    const { container } = render(<Icon name="check" />);
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(container.querySelector("svg")).toHaveAttribute("viewBox", "0 0 24 24");
  });
});

describe("status", () => {
  it("is a shape and a word, never colour alone", () => {
    render(<><Status state="ok" /><Status state="bad" /><Status state="wait" /><Status state="idle" /></>);
    expect(screen.getByRole("img", { name: "Passed" }).querySelector("svg")).not.toBeNull();
    expect(screen.getByRole("img", { name: "Failed" }).querySelector("svg")).not.toBeNull();
    expect(screen.getByRole("img", { name: "Waiting for a person" }).querySelector("svg")).not.toBeNull();
    expect(screen.getByRole("img", { name: "Not started" })).toBeInTheDocument();
  });
  it("maps the server's statuses to the design's states", () => {
    expect(stateOf("succeeded")).toBe("ok");
    expect(stateOf("failed")).toBe("bad");
    expect(stateOf("leased")).toBe("run");
    expect(stateOf("waiting")).toBe("wait");
    expect(stateOf("pending")).toBe("idle");
    expect(stateOfStation("done")).toBe("ok");
    expect(stateOfStation("waiting")).toBe("wait");
    expect(stateOfStation("whatever")).toBe("idle");
  });
  it("draws chips in a tone with an icon", () => {
    render(<><Chip tone="ok" icon="check">Healthy</Chip><StateChip state="bad" /></>);
    expect(screen.getByText("Healthy").className).toContain("ok");
    expect(screen.getByText("Failed").className).toContain("bad");
  });
});

describe("the launch line", () => {
  const stations = toStations([
    { key: "commit", label: "Commit", state: "done", detail: "a41f" }, { key: "tests", label: "Tests", state: "failed", detail: "1 failed" },
    { key: "verified", label: "Verified", state: "idle", detail: "" },
  ]);
  it("describes itself in words for a screen reader", () => {
    render(<LaunchLine stations={stations} />);
    expect(screen.getByRole("img", { name: "Commit: passed, Tests: failed, Verified: not yet" })).toBeInTheDocument();
  });
  it("makes each station a button when it can be chosen, and says which is chosen", async () => {
    const user = userEvent.setup();
    const pick = vi.fn();
    render(<LaunchLine stations={stations} big selected="tests" onSelect={pick} />);
    expect(screen.getByRole("button", { name: "Tests: failed. 1 failed" })).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByRole("button", { name: /Commit/ }));
    expect(pick).toHaveBeenCalledWith("commit");
  });
  it("gives the last station the green only when it passed", () => {
    const { container } = render(<LaunchLine stations={toStations([{ key: "a", label: "A", state: "done" }, { key: "v", label: "Verified", state: "done" }])} />);
    expect(container.querySelectorAll(".ls.last")).toHaveLength(1);
  });
});

describe("page parts", () => {
  it("opens a page with a title and one sentence", () => {
    render(<PageHead eyebrow="Area" title="Deployments" sentence="Everything is healthy." actions={<Button>Go</Button>} />);
    expect(screen.getByRole("heading", { level: 1, name: "Deployments" })).toBeInTheDocument();
    expect(screen.getByText("Everything is healthy.")).toBeInTheDocument();
  });
  it("draws a band whose cells are links, and loud only when told", () => {
    render(<Band cells={[{ key: "a", label: "Waiting", value: 2, href: "/activity/approvals", loud: "warn" }, { key: "b", label: "Failed", value: 0 }]} />);
    expect(screen.getByRole("link", { name: /Waiting/ })).toHaveClass("loud-w");
    expect(screen.getByText("Failed").closest(".band")?.children).toHaveLength(2);
  });
  it("shows an empty state: what this place is, and one action", () => {
    render(<EmptyState title="Nothing yet" action={<Button kind="pri">Add one</Button>}>This is where they appear.</EmptyState>);
    expect(screen.getByRole("heading", { name: "Nothing yet" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add one" })).toBeInTheDocument();
  });
  it("shows a decision with its evidence and actions", () => {
    render(<Decision title="Release 1.4.0" evidence={[{ key: "t", label: "Tests", value: 212 }]} actions={<Button kind="ok">Approve</Button>}>Low risk.</Decision>);
    expect(screen.getByRole("heading", { name: "Release 1.4.0" })).toBeInTheDocument();
    expect(screen.getByText("212")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toHaveClass("btn-ok");
  });
  it("lists evidence as terms and values", () => {
    const { container } = render(<EvidenceList items={[{ key: "a", label: "Passed", value: 3, note: "all" }]} />);
    expect(container.querySelector("dt")?.textContent).toBe("Passed");
  });
});

describe("the kit page", () => {
  it("shows every part", () => {
    render(<KitPage />);
    expect(screen.getByRole("heading", { level: 1 })).toBeInTheDocument();
    for (const h of ["Color and state", "Buttons and chips", "The launch line", "Empty state"]) expect(screen.getByRole("heading", { name: h })).toBeInTheDocument();
    expect(screen.getAllByRole("img").length).toBeGreaterThan(5);
  });
});

import { Tabs } from "./Tabs";
import { TrendStrip } from "./Trend";
import { trendOf } from "../guided/decide";

describe("Tabs and Trend", () => {
  it("moves between tabs with the arrow keys and marks the chosen one", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    let on = "a";
    const { rerender } = render(<Tabs label="Views" on={on} onChange={(id) => { on = id; }} tabs={[{ id: "a", label: "Live" }, { id: "b", label: "Text" }, { id: "c", label: "Draw", soon: true }]} />);
    expect(screen.getByRole("tab", { name: "Live" })).toHaveAttribute("aria-selected", "true");
    screen.getByRole("tab", { name: "Live" }).focus();
    await user.keyboard("{ArrowRight}");
    expect(on).toBe("b");
    rerender(<Tabs label="Views" on={on} onChange={() => {}} tabs={[{ id: "a", label: "Live" }, { id: "b", label: "Text" }]} />);
    expect(screen.getByRole("tab", { name: "Text" })).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("tab", { name: "Live" })).toHaveAttribute("tabindex", "-1");
  });
  it("describes a trend in words, never by colour alone", () => {
    render(<TrendStrip trend={trendOf([{ status: "failed", waiting: [], started_at: 1, created_at: 1, finished_at: 2 }, { status: "succeeded", waiting: [], started_at: 1, created_at: 1, finished_at: 2 }])} label="Last runs of shop" />);
    expect(screen.getByRole("img", { name: "Last runs of shop: 1 passed, 1 failed" })).toBeInTheDocument();
  });
});
