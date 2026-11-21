import { useEffect, useRef, useState } from "react";
import { useModels } from "../components/Explain";
import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { isLearn, locate, section, visibleSections, type Ctx } from "../nav/model";
import { comingFor } from "./comingData";
import { terms } from "../terms";
import { guideFor } from "./guideData";
import { learnFor } from "../learn/links";
import { LearnLink } from "../learn/LearnLink";
import type { Explainer } from "./explainer";

/** Guide is the calm right-hand column: where you are, in plain words, and what you can do next. The
 * optional AI help shows only when a model is configured. */
export function Guide({ ctx, path, explainer, onClose, focusOnOpen }: { ctx: Ctx; path: string; explainer: Explainer | null; onClose: () => void; focusOnOpen?: boolean }) {
  // Escape closes the panel from anywhere inside it; focus goes back to the button that opened it (Frame does that).
  const rail = useRef<HTMLElement>(null);
  // As a sheet over the page (a narrow screen) it takes focus, as any panel that covers the page does; docked beside it, it does not.
  useEffect(() => { if (focusOnOpen) rail.current?.querySelector<HTMLElement>("button")?.focus(); }, [focusOnOpen]);
  useEffect(() => {
    const el = rail.current;
    const key = (e: KeyboardEvent) => { if (e.key === "Escape") { e.stopPropagation(); onClose(); } };
    el?.addEventListener("keydown", key);
    return () => el?.removeEventListener("keydown", key);
  }, [onClose]);
  const at = locate(path);
  const s = at ? section(at.info.section) : null;
  const coming = comingFor(path);
  const g = at ? guideFor(at.info.pattern.replace(":view", at.params.view ?? ""), s?.label ?? "Launchway", s?.promise ?? "") : guideFor("", "Not here", "This page does not exist.");
  const text = coming ? `${coming.does} It is not built yet.` : g.text;
  return (
    <aside className="rail" aria-label={terms().label("guide")} ref={rail}>
      <div className="rail-top"><span className="tag">{terms().label("guide")}</span><button type="button" className="iconbtn" onClick={onClose} aria-label={`Close the ${terms().lower("guide")}`}><Icon name="x" /></button></div>
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
      {!isLearn(path) && (
        <>
          <div className="eyebrow rail-h">{terms().say("{Learn}")}</div>
          <LearnLink to={(at && learnFor(at.info.pattern, path)) || { slug: "", label: "Open Learn, the documentation" }} />
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

