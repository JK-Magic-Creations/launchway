import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import type { LogLine } from "../api";

// A small ANSI SGR renderer: colors, bright colors, bold, dim, underline.
const FG: Record<number, string> = {
  30: "a-k", 31: "a-r", 32: "a-g", 33: "a-y", 34: "a-b", 35: "a-m", 36: "a-c", 37: "a-w",
  90: "a-K", 91: "a-R", 92: "a-G", 93: "a-Y", 94: "a-B", 95: "a-M", 96: "a-C", 97: "a-W",
};

function renderAnsi(text: string): ReactNode {
  // eslint-disable-next-line no-control-regex
  const re = /\x1b\[([0-9;]*)m/g;
  if (!text.includes("\x1b[")) return text;
  const out: ReactNode[] = [];
  let last = 0;
  let cls: string[] = [];
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(<span key={i++} className={cls.join(" ")}>{text.slice(last, m.index)}</span>);
    last = re.lastIndex;
    const codes = m[1] === "" ? [0] : m[1].split(";").map(Number);
    for (const c of codes) {
      if (c === 0) cls = [];
      else if (c === 1) cls = [...cls.filter((x) => x !== "a-bold"), "a-bold"];
      else if (c === 2) cls = [...cls, "a-dim"];
      else if (c === 4) cls = [...cls, "a-u"];
      else if (c === 39) cls = cls.filter((x) => !x.startsWith("a-") || x === "a-bold" || x === "a-dim" || x === "a-u");
      else if (FG[c]) cls = [...cls.filter((x) => !(x in INV)), FG[c]];
    }
  }
  if (last < text.length) out.push(<span key={i++} className={cls.join(" ")}>{text.slice(last)}</span>);
  return out;
}
const INV: Record<string, true> = Object.fromEntries(Object.values(FG).map((v) => [v, true]));

const MAX_SHOWN = 5000;

export function LogView({ lines, live, empty }: { lines: LogLine[]; live: boolean; empty: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const [showAll, setShowAll] = useState(false);

  useLayoutEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);

  useEffect(() => { setFollow(true); setShowAll(false); }, [empty]);

  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setFollow(atBottom);
  };

  const hidden = showAll ? 0 : Math.max(0, lines.length - MAX_SHOWN);
  const shown = hidden ? lines.slice(hidden) : lines;

  return (
    <div className="logwrap">
      <div className="log" ref={box} onScroll={onScroll} role="log" aria-live={live ? "polite" : "off"}>
        {hidden > 0 && (
          <button type="button" className="log-more" onClick={() => setShowAll(true)}>
            Show {hidden.toLocaleString()} earlier lines
          </button>
        )}
        {shown.length === 0 && <div className="log-empty">{empty}</div>}
        {shown.map((l) => (
          <div key={l.seq} className={`ll ll-${l.stream}`}>
            <span className="ln" aria-hidden="true">{l.seq}</span>
            <span className="lt">{renderAnsi(l.text)}</span>
          </div>
        ))}
        {live && <div className="log-live"><span className="dot" /> live</div>}
      </div>
      {!follow && lines.length > 0 && (
        <button type="button" className="log-follow" onClick={() => setFollow(true)}>Jump to latest ↓</button>
      )}
    </div>
  );
}
