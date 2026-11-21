import { useState } from "react";
import { Icon } from "../kit/Icon";
import { ICON_PATHS, type IconName } from "../kit/iconPaths";
import { LaunchLine, type LineStation } from "../kit/LaunchLine";
import { Band, Button, Decision, DetailPanel, EmptyState, EvidenceList, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Chip, Status, StateChip, type State } from "../kit/Status";

// The design kit: every component of the interface, drawn with fixed example values so a person
// can see each in light and in dark. These are examples of the parts, not data of this installation.

const STATES: State[] = ["ok", "bad", "warn", "wait", "run", "idle", "skip"];

const STATIONS: LineStation[] = [
  { key: "commit", label: "Commit", state: "ok", detail: "a41f9c2a" },
  { key: "checks", label: "Checks", state: "ok", detail: "Passed" },
  { key: "tests", label: "Tests", state: "ok", detail: "212 passed" },
  { key: "package", label: "Package", state: "ok", detail: "Built" },
  { key: "dev", label: "dev", state: "ok", detail: "Version 1.4.0" },
  { key: "staging", label: "staging", state: "run", detail: "2 of 4 copies ready" },
  { key: "prod", label: "prod", state: "wait", detail: "Waiting for a decision" },
  { key: "verified", label: "Verified", state: "idle", detail: "Not yet" },
];

/** The kit page, at /kit. */
export function KitPage() {
  const [sel, setSel] = useState("staging");
  const picked = STATIONS.find((s) => s.key === sel);
  return (
    <div className="page kitpage">
      <PageHead eyebrow="Design kit" title="The parts every screen is built from"
        sentence="Each part is shown here with example values, in the theme you have chosen. Switch light and dark with the moon button at the top." />

      <Sheet title="Color and state" pad>
        <div className="kit-sw">
          {["--bg", "--sheet", "--sheet-3", "--line-2", "--ink", "--ink-3", "--signal", "--done", "--ok", "--warn", "--bad", "--idle"].map((t) => (
            <div key={t}><span className="sw" style={{ background: `var(${t})` }} /><code className="mono small">{t}</code></div>
          ))}
        </div>
        <p className="faint small">Neutral surfaces, one blue accent for things you can act on, and green only for health. A passed step is quiet slate, never green.</p>
      </Sheet>

      <Sheet title="Status: shape, color and a word" pad>
        <div className="row wrap" style={{ gap: 18 }}>
          {STATES.map((s) => <span key={s} className="row"><Status state={s} size="l" /><StateChip state={s} /></span>)}
        </div>
      </Sheet>

      <Sheet title="Buttons and chips" pad>
        <div className="row wrap">
          <Button kind="pri" icon="play">Start a run</Button><Button>Run again</Button><Button kind="ok" icon="check">Approve and release</Button>
          <Button kind="bad" icon="x">Reject</Button><Button kind="ghost">Quiet</Button><Button sm>Small</Button><Button disabled>Disabled</Button>
          <LinkButton href="/" icon="home">A link as a button</LinkButton>
        </div>
        <div className="row wrap" style={{ marginTop: 14 }}>
          <Chip>Neutral</Chip><Chip tone="ok" icon="check">Healthy</Chip><Chip tone="warn" icon="pause">Waiting</Chip><Chip tone="bad" icon="x">Failed</Chip><Chip tone="run">Running</Chip>
        </div>
      </Sheet>

      <Sheet title="The launch line" pad>
        <LaunchLine stations={STATIONS} big selected={sel} onSelect={setSel} />
        <p className="faint small" style={{ marginTop: 14 }}>{picked ? `${picked.label}: ${picked.detail}` : ""}</p>
        <div style={{ marginTop: 14 }}><LaunchLine stations={STATIONS.slice(0, 5)} compact label="A compact line, as in a row" /></div>
      </Sheet>

      <Band cells={[
        { key: "a", label: "Runs today", value: 24, foot: "2 still going" },
        { key: "b", label: "Waiting", value: 2, foot: "for a decision", loud: "warn" },
        { key: "c", label: "Failed", value: 1, foot: "since this morning", loud: "bad" },
        { key: "d", label: "Healthy places", value: "3 of 3", foot: "all clear" },
      ]} />

      <div className="dock">
        <Decision title="Release payments-api 1.4.0 to production" evidence={[
          { key: "t", label: "Tests", value: "212", note: "all passed" }, { key: "s", label: "Scan", value: "0", note: "serious findings" },
          { key: "r", label: "Risk", value: "Low" }, { key: "w", label: "Waiting", value: "3h" },
        ]} actions={<><Button kind="ok" icon="check">Approve and release</Button><Button kind="bad">Reject</Button></>}>
          Asked by a teammate. Every check passed.
        </Decision>
        <DetailPanel title="staging" status={<Status state="run" size="l" />}>
          <div className="facts">
            <div className="fact"><div className="q">What is this</div><div className="a">The place a change is tried before production.</div></div>
            <div className="fact"><div className="q">Now</div><div className="a">2 of 4 copies are ready.</div></div>
          </div>
        </DetailPanel>
      </div>

      <Sheet title="Evidence"><div className="sh-b"><EvidenceList items={[
        { key: "1", label: "Passed", value: 212 }, { key: "2", label: "Failed", value: 0 }, { key: "3", label: "Skipped", value: 3 },
      ]} /></div></Sheet>

      <Sheet title="Empty state"><EmptyState title="Nothing here yet" action={<LinkButton href="/" kind="pri" icon="plus">Add the first one</LinkButton>}>
        One sentence says what this place is and what will appear in it.</EmptyState></Sheet>

      <Sheet title={`Icons (${Object.keys(ICON_PATHS).length})`} pad>
        <div className="kit-icons">
          {(Object.keys(ICON_PATHS) as IconName[]).map((n) => <div key={n}><Icon name={n} size="l" /><span className="small">{n}</span></div>)}
        </div>
      </Sheet>
    </div>
  );
}
