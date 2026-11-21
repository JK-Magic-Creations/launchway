// What the "What do you want to do?" bar can find: the wizards and actions that exist, every page of
// every area, and the projects the person can see. Pure, so the search is tested without a browser.

import type { Project } from "../api";
import type { IconName } from "../kit/Icon";
import { WIZARDS } from "../components/ConfigNav";
import { visibleSections, visibleTabs, type Ctx } from "../nav/model";
import { terms } from "../terms";

export interface DoItem { id: string; label: string; hint: string; href: string; icon: IconName; group: "Do" | "Go to" | "Projects" }

/** Everything the bar can offer to this person. */
export function doItems(ctx: Ctx, projects: Project[]): DoItem[] {
  const out: DoItem[] = [];
  if (ctx.me.admin) {
    out.push({ id: "add-project", label: "Add a project", hint: "Connect a repository", href: "/config/wizard/connect-repository", icon: "plus", group: "Do" });
    for (const w of WIZARDS) {
      if (w.id === "connect-repository") continue;
      out.push({ id: "wizard-" + w.id, label: w.title, hint: "A short guided set-up", href: `/config/wizard/${w.id}`, icon: "plus", group: "Do" });
    }
  }
  out.push({ id: "decide", label: "Decide what is waiting", hint: terms().say("{Clearances} that need a person (approvals)"), href: "/activity/approvals", icon: "gate", group: "Do" });
  out.push({ id: "try", label: "Try a pipeline without saving", hint: "A scratch run", href: "/playground", icon: "play", group: "Do" });
  for (const p of projects) {
    if (p.last_run?.status === "failed") {
      out.push({ id: "why-" + p.name, label: `See why ${p.name} failed`, hint: `Run #${p.last_run.number}`, href: `/runs/${p.last_run.id}`, icon: "alert", group: "Do" });
    }
    out.push({ id: "project-" + p.name, label: p.name, hint: "Project", href: `/projects/${encodeURIComponent(p.name)}`, icon: "code", group: "Projects" });
  }
  for (const s of visibleSections(ctx)) {
    out.push({ id: "area-" + s.id, label: s.label, hint: s.promise, href: s.href, icon: s.icon, group: "Go to" });
    for (const t of visibleTabs(s, ctx)) {
      if (t.href === s.href) continue;
      out.push({ id: `page-${s.id}-${t.id}`, label: t.label + (t.soon ? " (soon)" : ""), hint: t.aka ? `${s.label} · ${t.aka}` : s.label, href: t.href, icon: t.icon ?? s.icon, group: "Go to" });
    }
  }
  return out;
}

/** The items that match what was typed: every word must appear in the label or hint. Nothing typed shows the actions. */
export function searchItems(items: DoItem[], q: string, limit = 12): DoItem[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return items.filter((i) => i.group === "Do").slice(0, limit);
  const scored: [number, DoItem][] = [];
  for (const it of items) {
    const label = it.label.toLowerCase();
    const hay = label + " " + it.hint.toLowerCase();
    if (!words.every((w) => hay.includes(w))) continue;
    const starts = words.every((w) => label.split(/\s+/).some((x) => x.startsWith(w))) ? 0 : 1;
    scored.push([starts * 2 + (it.group === "Do" ? 0 : it.group === "Go to" ? 1 : 1), it]);
  }
  return scored.sort((a, b) => a[0] - b[0]).slice(0, limit).map((x) => x[1]);
}
