import { useId, useState, type ReactNode } from "react";
import { useTerms } from "../terms";

/**
 * Term renders a product word with its plain meaning reachable without a mouse.
 *
 * The pattern, chosen once for every page: the word is a button with a dotted underline (the affordance), so it
 * takes keyboard focus and Enter or a tap pins the tip (touch has no hover); its plain meaning is a tooltip that shows on hover and on focus, stays while the pointer is
 * on it, and goes away with Escape (WCAG 1.4.13). The meaning is always in the page for assistive technology
 * through aria-describedby, so it never depends on hover. To avoid a page full of dotted words, a Term is
 * placed once where a word is introduced (the heading or first sentence of a page, an empty state), not on
 * every occurrence; in a list or a table the plain word is shown beside the themed one instead.
 * In the plain business set the word is already plain, so it renders as ordinary text.
 */
export function Term({ k, children, plural }: { k: string; children?: ReactNode; plural?: boolean }) {
  const t = useTerms();
  const id = useId();
  const [dismissed, setDismissed] = useState(false);
  const [pinned, setPinned] = useState(false);
  const word = children ?? (plural ? t.plural(k) : t.word(k));
  if (!t.themed(k)) return <>{word}</>;
  const cls = "term" + (dismissed ? " dismissed" : "") + (pinned ? " pinned" : "");
  return (
    <span className={cls} onMouseLeave={() => setDismissed(false)}>
      <button type="button" className="term-w" aria-describedby={id} aria-expanded={pinned}
        onClick={() => { setPinned(!pinned); setDismissed(false); }}
        onBlur={() => { setDismissed(false); setPinned(false); }}
        onKeyDown={(e) => { if (e.key === "Escape") { setDismissed(true); setPinned(false); } }}>{word}</button>
      <span id={id} role="tooltip" className="term-tip"><b>In plain words: {t.plain(k).toLowerCase()}.</b> {t.meaning(k)}</span>
    </span>
  );
}
