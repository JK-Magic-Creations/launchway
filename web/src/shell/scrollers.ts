import { useEffect } from "react";

// A region that scrolls (a wide table, a code block, a drawing) must be reachable by keyboard, or a person who does not
// use a mouse cannot read the part that is cut off (WCAG 2.1.1). Pages draw such regions in many places; this finds
// them once, in the shell, and makes each one focusable when it really overflows and holds nothing focusable itself.

const CANDIDATES = ".table-scroll, .scroll, table, pre, figure, .lx-diagram, .code, .excerpt, [class*='scroll']";
const FOCUSABLE = "a[href], button, input, select, textarea, summary, [tabindex]";

export function makeScrollersFocusable(root: ParentNode): void {
  const used = new Map<string, number>();
  for (const e of root.querySelectorAll<HTMLElement>("[data-lw-scroll=\"1\"][aria-label]")) { const b = e.getAttribute("aria-label")!.replace(/ \(\d+\)$/, ""); used.set(b, (used.get(b) ?? 0) + 1); }
  for (const el of root.querySelectorAll<HTMLElement>(CANDIDATES)) {
    const s = getComputedStyle(el);
    const scrolls = /(auto|scroll)/.test(s.overflowX + s.overflowY) && (el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1);
    const ours = el.dataset.lwScroll === "1";
    if (scrolls && !ours && !el.hasAttribute("tabindex") && !el.querySelector(FOCUSABLE)) {
      el.tabIndex = 0;
      el.dataset.lwScroll = "1";
      // A table keeps its table role; a plain box becomes a named region so the stop is announced.
      if (el.tagName !== "TABLE" && !el.getAttribute("role") && !el.hasAttribute("aria-label")) {
        el.setAttribute("role", "region");
        // Named after the nearest heading, and numbered when several regions would share a name.
        const head = el.closest("section, .card, article")?.querySelector("h1, h2, h3")?.textContent?.trim();
        const base = el.tagName === "PRE" ? "Code, scrolls sideways" : head ? `${head}, scrolls sideways` : "Content that scrolls sideways";
        const n = (used.get(base) ?? 0) + 1;
        used.set(base, n);
        el.setAttribute("aria-label", n > 1 ? `${base} (${n})` : base);
      }
    } else if (!scrolls && ours) {
      el.removeAttribute("tabindex");
      delete el.dataset.lwScroll;
    }
  }
}

/** Keeps scrolling regions under `main` focusable as the page changes and is resized. */
export function useFocusableScrollers(): void {
  useEffect(() => {
    let frame = 0;
    const run = () => { frame = 0; const main = document.getElementById("main"); if (main) makeScrollersFocusable(main); };
    const later = () => { if (!frame) frame = requestAnimationFrame(run); };
    const mo = new MutationObserver(later);
    const main = document.getElementById("main");
    if (main) mo.observe(main, { childList: true, subtree: true });
    window.addEventListener("resize", later);
    later();
    return () => { mo.disconnect(); window.removeEventListener("resize", later); if (frame) cancelAnimationFrame(frame); };
  }, []);
}
