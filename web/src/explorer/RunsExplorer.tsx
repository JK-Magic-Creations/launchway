import { useMemo } from "react";
import type { RunSummary } from "../api";
import { Explorer } from "./Explorer";
import { useOrganized } from "./useOrganized";
import type { Seg, Tone, XItem } from "./tree";
import { refLabel } from "../format";

const TONE: Record<string, Tone> = { succeeded: "ok", failed: "bad", running: "run", queued: "run" };
const WORD: Record<string, string> = { succeeded: "Passed", failed: "Failed", running: "Running", queued: "Queued", canceled: "Canceled" };

/** A pipeline step under its Flight, only when the pipeline is named differently from the Flight: today a Flight has one
 * pipeline named like it, and a node with the same name twice would only add a click. The key holds no "/" (keys are joined with one). */
const pipelineSeg = (flight: string, pipeline?: string): Seg[] => (pipeline && pipeline !== flight ? [{ key: "pl:" + flight.replace(/\//g, "|") + "|" + pipeline.replace(/\//g, "|"), label: pipeline }] : []);

/** The Sorties as an Explorer: the tree follows Wing > Squadron (or Unfiled) > Flight > pipeline, so one pipeline's Sorties are one place. */
export default function RunsExplorer({ runs, now, me }: { runs: RunSummary[]; now: number; me: string }) {
  const org = useOrganized("runs");
  const items = useMemo<XItem[]>(() => runs.map((r) => {
    const where = org.place(r.project);
    const waiting = (r.waiting?.length ?? 0) > 0 && r.status !== "succeeded" && r.status !== "failed" && r.status !== "canceled";
    return {
      id: r.id, name: `${r.project} #${r.number}`, description: r.message || r.pipeline, path: [...where.path.filter((p) => !p.folderId), { key: "p:" + r.project, label: r.project }, ...pipelineSeg(r.project, r.pipeline)],
      labels: [], kind: r.event, tone: waiting ? "wait" : TONE[r.status] ?? "none", history: [], running: r.status === "running" ? 1 : 0, href: `/runs/${r.id}`, favorite: false,
      mine: r.actor === me, last: { text: `${WORD[r.status] ?? r.status}${refLabel(r) ? ` · ${refLabel(r)}` : ""}`, at: r.finished_at || r.created_at },
      actions: [{ label: "Open", href: `/runs/${r.id}` }],
    };
  }), [runs, org, me]);
  const kinds = useMemo(() => [...new Set(runs.map((r) => r.event))].filter(Boolean).sort(), [runs]);
  return <Explorer noun={{ singular: "Sortie", plural: "Sorties" }} items={items} collections={org.collections} onCollections={org.saveCollections} now={now} kinds={kinds} strip={false} empty={<p>No Sortie matches.</p>} />;
}
