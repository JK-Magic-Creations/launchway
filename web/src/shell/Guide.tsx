import { createContext, useContext, useEffect, useState } from "react";
import { useModels } from "../components/Explain";
import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { locate, section, visibleSections, type Ctx } from "../nav/model";
import { COMING } from "./comingData";
import { guideFor } from "./guideData";

/** A page that can offer its own explanation (the run page, for a failed step) puts it here; the Guide
 * shows it as a button, only when a model is configured. */
export interface Explainer { label: string; run: () => Promise<string> }
export const ExplainerContext = createContext<(e: Explainer | null) => void>(() => {});
export function useRegisterExplainer(e: Explainer | null): void {
  const set = useContext(ExplainerContext);
  const label = e?.label;
  useEffect(() => { set(e); return () => set(null); }, [label]); // eslint-disable-line react-hooks/exhaustive-deps
}

/** Guide is the calm right-hand column: where you are, in plain words, and what you can do next. The
 * optional AI help shows only when a model is configured. */
export function Guide({ ctx, path, explainer, onClose }: { ctx: Ctx; path: string; explainer: Explainer | null; onClose: () => void }) {
  const at = locate(path);
  const s = at ? section(at.info.section) : null;
  const coming = COMING[path];
  const g = at ? guideFor(at.info.pattern, s?.label ?? "Launchway", s?.promise ?? "") : guideFor("", "Not here", "This page does not exist.");
  const text = coming ? `${coming.does} It is not built yet.` : g.text;
  return (
    <aside className="rail" aria-label="Guide">
      <div className="rail-top"><span className="tag">Guide</span><button type="button" className="iconbtn" onClick={onClose} aria-label="Close the guide"><Icon name="x" /></button></div>
      <h2>{coming ? coming.title : g.title}</h2>
      <p>{text}</p>
      {(g.does?.length ?? 0) > 0 && !coming && (
        <>
          <div className="eyebrow rail-h">What you can do</div>
          <div className="do">
            {g.does!.map((d) => <a key={d.href} href={d.href} onClick={onLink}><Icon name={d.icon} />{d.label}<Icon name="arrow" size="s" className="go" /></a>)}
          </div>
        </>
      )}
      {at?.info.pattern === "/" && (
        <>
          <div className="eyebrow rail-h">Where things are</div>
          <div className="where">
            {visibleSections(ctx).filter((x) => x.id !== "home").map((x) => (
              <a key={x.id} href={x.href} onClick={onLink}><Icon name={x.icon} size="s" /><span><b>{x.label}</b>{x.promise}</span></a>
            ))}
          </div>
        </>
      )}
      <Ask key={explainer?.label ?? ""} explainer={explainer} />
    </aside>
  );
}

function Ask({ explainer }: { explainer: Explainer | null }) {
  const { models } = useModels();
  const [answer, setAnswer] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Nothing is shown unless a model is configured: the page never advertises what is switched off.
  if (!models || models.models.length === 0) return null;
  const ask = async () => {
    if (!explainer) return;
    setBusy(true); setError("");
    try { setAnswer(await explainer.run()); } catch (e) { setError(e instanceof Error ? e.message : "The model did not answer."); }
    setBusy(false);
  };
  return (
    <div className="ask">
      <div className="eyebrow">Ask a model</div>
      {explainer ? (
        <button type="button" className="btn btn-sm" disabled={busy} onClick={ask}><Icon name="spark" size="s" />{busy ? "Asking…" : explainer.label}</button>
      ) : (
        <p className="hint">A model is set up. Open a step that failed and ask it to explain, in plain words.</p>
      )}
      {error && <p role="alert" className="hint">{error}</p>}
      {answer && <div className="bubble"><div className="tag">A model's answer. It may be wrong.</div>{answer}</div>}
    </div>
  );
}

