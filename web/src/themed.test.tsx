import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { installServer } from "./test/mockApi";
import { resetTerms, setTerms, vocabulary } from "./terms";
import guard from "../../docs/vocabulary.json";

// The two sets switch EVERYTHING together. Key pages are drawn whole in each set: the business set shows no
// aviation word, and the aviation set shows no superseded plain word (the same rules `make vocabulary` applies
// to the source). See docs/reference/vocabulary.md.
const PAGES = ["/", "/activity", "/runs/run_7", "/settings/runners", "/deployments", "/deployments/environments", "/deployments/environments/prod", "/settings/words", "/learn", "/traffic"];

/** The aviation words that cannot be mistaken for ordinary English (a capitalized name, whole word). */
const AMBIGUOUS = new Set(["Go", "Hold", "Base", "Wing", "Switch", "Launch", "Abort", "Holding", "Failed", "Waiting", "Project", "Checklist", "Payload", "Debrief", "Drill", "Maintenance", "Inspection", "Cargo", "Depot"]);
function aviationWords(): RegExp {
  const forms = new Set<string>();
  for (const w of vocabulary()) {
    if (w.aviation.one === w.plain.one || w.group === "levels") continue;
    for (const f of [w.aviation.one, w.aviation.many]) if (f && !AMBIGUOUS.has(f)) forms.add(f);
  }
  return new RegExp("(?<![\\w-])(?:" + [...forms].map((f) => f.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|") + ")(?![\\w-])");
}

/** What a person reads on the page: every text node, except the tooltips (which are meant to name the plain word). */
function readable(): string[] {
  const out: string[] = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || el.closest(".term-tip, .words, script, style, [aria-hidden='true']")) continue;
    const t = (n.textContent ?? "").trim();
    if (t) out.push(t);
  }
  return out;
}

/** Text people wrote (a fixture's approval message, a pipeline's question), not the product's. */
const USER_DATA = /Deploy (a second version|this build)|^Waiting for you: Deploy/;
const wrap = (p: string) => new RegExp("(?<![\\w\\-./{])(?:" + p + ")(?![\\w-]|\\.\\w|/)", "i");
const rules = (guard.superseded as unknown as { key: string; rules: { pattern: string; auto?: boolean; web?: boolean }[]; labels: Record<string, string> }[])
  .flatMap((s) => s.rules.filter((r) => r.auto || r.web).map((r) => ({ key: s.key, re: wrap(r.pattern) })));
const captions = (guard.superseded as unknown as { key: string; labels: Record<string, string> }[]).flatMap((s) => Object.entries(s.labels).filter(([, v]) => v).map(([l]) => ({ key: s.key, label: l })));

describe.each(["business", "aviation"] as const)("the %s set on key pages", (set) => {
  beforeEach(() => { vi.stubGlobal("EventSource", class { close() {} addEventListener() {} removeEventListener() {} onmessage = null; onerror = null; }); HTMLCanvasElement.prototype.getContext = (() => null) as never; installServer({ "/api/v1/config": { available: true, kinds: [], admin: true, terms: { set, overrides: {} } } }); setTerms({ set }); });
  afterEach(() => { cleanup(); resetTerms(); vi.unstubAllGlobals(); });
  it.each(PAGES)("%s", async (path) => {
    history.replaceState(null, "", path);
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 150));
    const text = readable();
    if (set === "business") {
      const av = aviationWords();
      expect(text.filter((t) => av.test(t)), `${path}: aviation words in the business set`).toEqual([]);
    } else {
      const bad: string[] = [];
      for (const t of text) {
        if (USER_DATA.test(t)) continue;
        for (const r of rules) if (r.re.test(t)) bad.push(`${r.key}: "${t.slice(0, 90)}"`);
        for (const c of captions) if (t === c.label) bad.push(`${c.key}: caption "${t}"`);
      }
      expect(bad, `${path}: superseded plain words in the aviation set`).toEqual([]);
    }
  }, 20000);
});
