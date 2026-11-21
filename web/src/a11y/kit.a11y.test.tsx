import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Dialog } from "../kit/Dialog";
import { LaunchLine, type LineStation } from "../kit/LaunchLine";
import { Band, Button, Decision, DetailPanel, EmptyState, EvidenceList, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Chip, StateChip, Status, type State } from "../kit/Status";
import { Tabs } from "../kit/Tabs";
import { Announcer } from "../kit/announce";
import { DoBar } from "../shell/DoBar";
import { Launcher } from "../shell/Launcher";
import { UserMenu } from "../shell/UserMenu";
import { NO_FACTS } from "../nav/model";
import { expectAccessible } from "../test/axe";
import { me, project, run } from "../test/fixtures";
import { DIAMOND, FAILED_WITH_SKIPPED, FAN_OUT, LONG_CHAIN, MIXED, MIXED_GROUPS, NOW, OWN_PIPELINE } from "../test/graphFixtures";
import { Graph } from "../components/Graph";

const STATES: State[] = ["ok", "bad", "warn", "wait", "run", "idle", "skip"];
const stations: LineStation[] = STATES.map((s) => ({ key: s, label: "Station " + s, state: s, detail: s === "idle" ? undefined : "detail of " + s }));
const ctx = { me: me(), facts: NO_FACTS };

// Every part of the kit and the shell's overlays, alone, in both themes. Colour contrast is not computed by jsdom: see
// styles/contrast.test.ts (the tokens) and web/scripts/a11y-browser.mjs (the pixels).
describe.each(["light", "dark"])("the kit, %s theme", (theme) => {
  beforeEach(() => { document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => document.documentElement.removeAttribute("data-theme"));

  it("page parts", async () => {
    const { container } = render(
      <main>
        <PageHead eyebrow="Eyebrow" title="Title" sentence="One plain sentence." actions={<Button kind="pri" icon="play">Start</Button>} />
        <Band cells={[{ key: "a", label: "Runs", value: 3, href: "/activity" }, { key: "b", label: "Failed", value: 1, loud: "bad" }]} />
        <Sheet title="A sheet" pad><LinkButton href="/x" kind="ghost" icon="arrow">Go</LinkButton></Sheet>
        <EmptyState title="Nothing yet" action={<Button>Add</Button>}>What will appear here.</EmptyState>
        <Decision title="Deploy to production" evidence={[{ key: "t", label: "Tests", value: "212", note: "all passed" }]} actions={<Button kind="ok">Go</Button>}>Waiting for you.</Decision>
        <EvidenceList items={[{ key: "a", label: "A", value: 1 }]} />
        <DetailPanel title="Details of one" status={<Status state="ok" />}>Text</DetailPanel>
      </main>,
    );
    await expectAccessible(container, { name: "page parts" });
  });

  it("status marks, chips and the launch line, in every state", async () => {
    const { container } = render(
      <main>
        <h1>States</h1>
        {STATES.map((s) => <p key={s}><Status state={s} size="l" /> <StateChip state={s} /></p>)}
        <Chip tone="ok" icon="check">Passed</Chip>
        <LaunchLine stations={stations} big />
        <LaunchLine stations={stations} compact />
        <LaunchLine stations={stations} selected="ok" onSelect={() => {}} />
      </main>,
    );
    await expectAccessible(container, { name: "states" });
  });

  it("the pipeline graph, with steps running together, skipped, waiting, failed and folded", async () => {
    const { container } = render(
      <main>
        <h1>Graphs</h1>
        {[OWN_PIPELINE, FAN_OUT, DIAMOND, LONG_CHAIN, FAILED_WITH_SKIPPED].map((steps, i) => <Graph key={i} steps={steps} selected="test" onSelect={() => {}} now={NOW} />)}
        <Graph steps={MIXED} groups={MIXED_GROUPS} selected={null} onSelect={() => {}} now={NOW} />
        <Graph steps={OWN_PIPELINE} selected={null} onSelect={() => {}} now={0} compact describe={(x) => x.container ?? "image"} />
        <LaunchLine stations={[{ key: "c", label: "Checks", state: "run", detail: "running", parallel: { total: 3, together: 3, done: 2, running: 1, waiting: 0, failed: 0, skipped: 0, idle: 0,
          steps: [{ id: "a", state: "done" }, { id: "b", state: "done" }, { id: "c", state: "running" }] } }, { key: "d", label: "Deploy", state: "idle" }]} compact />
      </main>,
    );
    await expectAccessible(container, { name: "graph" });
  });

  it("tabs, a dialog, the search palette, the areas list and the user menu", async () => {
    const { container } = render(
      <main>
        <h1>Overlays</h1>
        <Tabs label="Views" on="a" onChange={() => {}} tabs={[{ id: "a", label: "Live" }, { id: "b", label: "Text" }]} />
        <div role="tabpanel" id="p" aria-labelledby="tab-a">Panel</div>
        <UserMenu me={me()} />
        <Dialog label="A dialog" onClose={() => {}}><h2>Inside</h2><Button>Close</Button></Dialog>
        <DoBar ctx={ctx} projects={[project()]} onClose={() => {}} />
        <Launcher ctx={ctx} onClose={() => {}} />
        <Announcer />
      </main>,
    );
    await expectAccessible(container, { name: "overlays", allow: ["aria-dialog-name"] });
  });
});

describe("status is never carried by color alone", () => {
  it("gives every state a name a screen reader reads, and a shape or a word a sighted reader sees", () => {
    render(<>{STATES.map((s) => <span key={s} data-s={s}><Status state={s} /><StateChip state={s} /></span>)}</>);
    for (const s of STATES) {
      const row = document.querySelector(`[data-s="${s}"]`)!;
      const mark = row.querySelector('[role="img"]')!;
      expect(mark.getAttribute("aria-label"), s).toMatch(/\S/);
      // The chip carries the same state as text, so the mark is never the only carrier.
      expect(row.querySelector(".vchip")!.textContent, s).toMatch(/\S/);
    }
  });
  it("draws a different shape for states that have no glyph: spinner, dashed ring, plain ring", () => {
    const { container } = render(<>{(["run", "idle", "skip"] as State[]).map((s) => <Status key={s} state={s} />)}</>);
    const classes = [...container.querySelectorAll(".st")].map((e) => e.className);
    expect(new Set(classes).size).toBe(3);
  });
  it("says every launch-line station's state in text", () => {
    render(<LaunchLine stations={stations} compact />);
    const items = screen.getAllByRole("listitem").map((i) => i.textContent ?? "");
    for (const s of STATES) expect(items.join("|")).toContain(`Station ${s}: `);
  });
  it("keeps the run's failed state a word wherever a run is listed", () => {
    render(<StateChip state="bad" />);
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(run({ status: "failed" }).status).toBe("failed");
  });
});
