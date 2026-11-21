import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import type { LogLine } from "../api";
import { useNow } from "../live";
import "../estimate.css";

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

// How many lines are drawn at once: the newest ones. Earlier lines are one click away, in steps, so a log of
// hundreds of thousands of lines stays fast to scroll and to update.
const WINDOW = 1500;
const STEP = 3000;
// A step that has printed nothing for this long is said to be quiet, so silence is visibly silence.
const QUIET_MS = 10_000;

/** LogView shows a step's output. New lines are appended without drawing the rest again; the view follows
 * the end until the person scrolls up (then it stays put and offers "Jump to latest"). While the step runs it
 * says "live" and when it last printed. Screen readers get a summary every ten seconds, not each line. */
export function LogView({ lines, live, empty, dropped = 0, now: nowProp }: { lines: LogLine[]; live: boolean; empty: string; dropped?: number; now?: number }) {
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const [window_, setWindow] = useState(WINDOW);
  const clock = useNow(1000, live && nowProp === undefined);
  const now = nowProp ?? clock;
  const [spoken, setSpoken] = useState("");

  useLayoutEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);

  useEffect(() => { setFollow(true); setWindow(WINDOW); }, [empty]);

  // A summary for assistive technology, at most every ten seconds.
  const count = useRef(0);
  useEffect(() => { count.current = lines.length; });
  useEffect(() => {
    if (!live) return;
    const say = () => setSpoken(`Live. ${count.current.toLocaleString()} lines so far.`);
    say();
    const t = setInterval(say, 10_000);
    return () => clearInterval(t);
  }, [live]);

  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 24);
  };

  const hidden = Math.max(0, lines.length - window_);
  const shown = hidden ? lines.slice(hidden) : lines;
  const lastAt = lines.length ? lines[lines.length - 1].at : 0;
  const quiet = live && lastAt > 0 && now - lastAt > QUIET_MS;
  const silence = lastAt > 0 ? Math.max(0, Math.floor((now - lastAt) / 1000)) : 0;

  return (
    <div className="logwrap">
      <div className="log" ref={box} onScroll={onScroll} role="log" aria-live="off" aria-label="Output">
        {dropped > 0 && (
          <p className="log-note">{dropped.toLocaleString()} earlier lines are not kept in this view to keep the page fast; all of them are in the full log, which the API and the command line serve.</p>
        )}
        {hidden > 0 && (
          <button type="button" className="log-more" onClick={() => setWindow((w) => w + STEP)}>
            Show {Math.min(hidden, STEP).toLocaleString()} earlier lines ({hidden.toLocaleString()} not drawn)
          </button>
        )}
        {shown.length === 0 && <div className="log-empty">{empty}</div>}
        {shown.map((l) => (
          <div key={l.seq} className={`ll ll-${l.stream}`}>
            <span className="ln" aria-hidden="true">{l.seq}</span>
            <span className="lt">{renderAnsi(l.text)}</span>
          </div>
        ))}
        {live && (
          <div className={"log-status" + (quiet ? " quiet" : "")}>
            <span className="dot" aria-hidden="true" />
            <span>{quiet ? "live, but quiet" : "live"}</span>
            {lastAt > 0 && <span>{silence < 2 ? "last output just now" : `last output ${silence < 60 ? `${silence}s` : `${Math.floor(silence / 60)}m ${silence % 60}s`} ago`}</span>}
            {!follow && <span className="log-paused">paused while you read; new lines keep arriving</span>}
          </div>
        )}
      </div>
      <p className="visually-hidden" role="status">{live ? spoken : ""}</p>
      {!follow && lines.length > 0 && (
        <button type="button" className="log-follow" onClick={() => setFollow(true)}>Jump to latest ↓</button>
      )}
    </div>
  );
}
