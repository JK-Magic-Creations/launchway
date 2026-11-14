import { forwardRef, useImperativeHandle, useMemo, useRef } from "react";
import type { Problem } from "../api";

export interface PipelineEditorHandle {
  /** Puts the cursor on a line and scrolls it into view. */
  goToLine: (line: number) => void;
}

interface Props {
  text: string;
  onChange: (text: string) => void;
  problems?: Problem[];
  label: string;
  readOnly?: boolean;
}

const LINE_HEIGHT = 13 * 1.6; // matches .editor textarea: 13px font, line-height 1.6

// PipelineEditor is a plain text area with line numbers. Lines with problems
// are marked in the gutter and behind the text, with the problem as a
// tooltip, so the place to fix is visible while typing.
export const PipelineEditor = forwardRef<PipelineEditorHandle, Props>(function PipelineEditor(
  { text, onChange, problems, label, readOnly }, ref) {
  const area = useRef<HTMLTextAreaElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const marks = useRef<HTMLDivElement>(null);
  const lines = text.split("\n").length;

  const byLine = useMemo(() => {
    const m = new Map<number, string[]>();
    for (const p of problems ?? []) {
      if (!p.line) continue;
      m.set(p.line, [...(m.get(p.line) ?? []), (p.path ? p.path + ": " : "") + p.message]);
    }
    return m;
  }, [problems]);

  const sync = () => {
    const ta = area.current;
    if (!ta) return;
    if (gutter.current) gutter.current.scrollTop = ta.scrollTop;
    if (marks.current) marks.current.scrollTop = ta.scrollTop;
  };

  useImperativeHandle(ref, () => ({
    goToLine(line: number) {
      const ta = area.current;
      if (!ta) return;
      const all = ta.value.split("\n");
      const n = Math.min(Math.max(line, 1), all.length);
      let start = 0;
      for (let i = 0; i < n - 1; i++) start += all[i].length + 1;
      ta.focus();
      ta.setSelectionRange(start, start + all[n - 1].length);
      ta.scrollTop = Math.max(0, (n - 1) * LINE_HEIGHT - ta.clientHeight / 3);
      sync();
    },
  }), []);

  return (
    <div className="editor">
      <div className="editor-gutter" ref={gutter} aria-hidden="true">
        <div className="editor-gutter-inner">
          {Array.from({ length: lines }, (_, i) => {
            const msgs = byLine.get(i + 1);
            return <div key={i} className={msgs ? "gutter-bad" : ""} title={msgs?.join("\n")}>{i + 1}</div>;
          })}
        </div>
      </div>
      <div className="editor-area">
        <div className="editor-marks" ref={marks} aria-hidden="true">
          <div className="editor-marks-inner">
            {Array.from({ length: lines }, (_, i) => <div key={i} className={byLine.has(i + 1) ? "mark-bad" : ""} />)}
          </div>
        </div>
        <textarea ref={area} aria-label={label} spellCheck={false} value={text} readOnly={readOnly}
          autoCapitalize="off" autoComplete="off" autoCorrect="off"
          aria-invalid={byLine.size > 0 || undefined}
          onScroll={sync}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            // Tab indents by two spaces; Escape then Tab leaves the editor.
            if (e.key === "Escape") { e.currentTarget.blur(); return; }
            if (e.key === "Tab" && !e.shiftKey && !readOnly) {
              e.preventDefault();
              const el = e.currentTarget, s = el.selectionStart, end = el.selectionEnd;
              onChange(text.slice(0, s) + "  " + text.slice(end));
              requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = s + 2; });
            }
          }} />
      </div>
    </div>
  );
});
