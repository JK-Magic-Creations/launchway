import { Mono } from "../components/ui";
import { Markdown } from "../markdown";
import type { CustomStepRecord, StepNote } from "../stepsApi";

/** What a run recorded about a custom step: where it came from and what it may do, and what it wrote besides outputs.
 *  Markdown goes through the product's own sanitizing renderer. */
export function CustomStepPanel({ custom, notes }: { custom?: CustomStepRecord; notes?: StepNote[] }) {
  const summaries = (notes ?? []).filter((n) => n.kind === "summary");
  const annotations = (notes ?? []).filter((n) => n.kind === "annotation");
  return (
    <div className="custom-step">
      {custom && (
        <p className="faint">
          Custom step <Mono>{custom.name}</Mono> from {custom.library === "self" ? "this repository" : <>library <Mono>{custom.library}</Mono></>}
          {custom.commit && <> at <Mono>{custom.commit.slice(0, 8)}</Mono></>}. {permissions(custom)}
        </p>
      )}
      {summaries.map((n, i) => <div key={i} className="callout"><Markdown text={n.text} /></div>)}
      {annotations.length > 0 && (
        <ul aria-label="Notes on files">
          {annotations.map((n, i) => <li key={i}><strong>{n.level}</strong> <Mono>{n.file}{n.line ? `:${n.line}` : ""}</Mono> {n.text}</li>)}
        </ul>
      )}
    </div>
  );
}

function permissions(c: CustomStepRecord): string {
  const net = c.network?.length ? `reach ${c.network.join(", ")}` : "reach no network";
  const sec = c.secrets?.length ? `read the secrets given as ${c.secrets.join(", ")}` : "read no secrets";
  const w = !c.write?.length ? "write only its own output files" : c.write.includes(".") ? "change any file in the workspace" : `write ${c.write.join(", ")}`;
  return `This step can ${net}, ${sec} and ${w}.`;
}
