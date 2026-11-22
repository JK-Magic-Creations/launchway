import { useEffect, useRef, useState } from "react";
import { PipelineEditor, type PipelineEditorHandle } from "../components/PipelineEditor";
import { PipelinePreview } from "../components/PipelinePreview";
import { Link } from "../components/ui";
import { KeyHelp } from "../learn/LearnLink";
import { useValidation } from "../validation";
import { terms } from "../terms";
import { prose } from "../prose";
import { PSub } from "../kit/bits";
const P = await prose("pages.Playground");

const SAMPLE = P.t0;

const KEY = "launchway.playground";
const EXPAND_KEY = "launchway.playground.expand";

// The playground checks a pipeline as you type and draws the graph it
// produces. Nothing runs, and nothing is saved anywhere but this browser.
export function PlaygroundPage() {
  const [text, setText] = useState(() => {
    try { return localStorage.getItem(KEY) || SAMPLE; } catch { return SAMPLE; }
  });
  // Expanding reads the server's libraries; it is a choice, since a pipeline
  // written here may name libraries the server does not have.
  const [expand, setExpand] = useState(() => {
    try { return localStorage.getItem(EXPAND_KEY) === "1"; } catch { return false; }
  });
  const { res, error } = useValidation(text, { expand });
  useEffect(() => {
    try { localStorage.setItem(EXPAND_KEY, expand ? "1" : "0"); } catch { /* private mode */ }
  }, [expand]);
  const editor = useRef<PipelineEditorHandle>(null);
  const [cursorLine, setCursorLine] = useState("");
  useEffect(() => {
    try { localStorage.setItem(KEY, text); } catch { /* private mode */ }
  }, [text]);

  return (
    <div className="page page-wide">
      <header className="page-head">
        <div>
          <h1>Playground</h1>
          <PSub>{terms().say(P.t1)} <Link href="/projects">{terms().say("{Projects}")}</Link>.</PSub>
        </div>
        <div className="run-actions">
          <label className="checkline" title={P.t2}>
            <input type="checkbox" checked={expand} onChange={(e) => setExpand(e.target.checked)} /> Expand templates
          </label>
          <KeyHelp line={cursorLine} />
          <button type="button" className="btn" onClick={() => setText(SAMPLE)}>Reset example</button>
        </div>
      </header>
      <div className="split split-play">
        <section className="card card-flush">
          <PipelineEditor ref={editor} text={text} onChange={setText} problems={res?.ok ? [] : res?.problems} label="Pipeline file" onCursorLine={setCursorLine} />
        </section>
        <PipelinePreview res={res} error={error} onJump={(line) => editor.current?.goToLine(line)} />
      </div>
    </div>
  );
}
