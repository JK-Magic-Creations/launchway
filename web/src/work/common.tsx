import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api";
import { Link } from "../components/ui";
import { Chip, type Tone } from "../kit/Status";
import { Button, PageHead } from "../kit/layout";
import { terms } from "../terms";
import type { Item } from "./workApi";
import "./work.css";

// Pieces every work page uses. Nothing here is in the first download: only the lazy work pages import it.

const enc = encodeURIComponent;

/** The address of a work page for a scope such as project:acme/shop. */
export const scopeHref = (scope: string, page = "items") => `/work/${enc(scope)}/${page}`;
export const itemHref = (ref: string) => `/work/item/${enc(ref)}`;

/** What a scope is called on screen: a project by its path, the higher levels by the installation's words. */
export function scopeLabel(scope: string): string {
  const i = scope.indexOf(":");
  const kind = i < 0 ? scope : scope.slice(0, i);
  const id = i < 0 ? "" : scope.slice(i + 1);
  if (kind === "project") return id;
  const t = terms();
  const word = kind === "installation" ? t.label("installation") : kind === "unit" ? t.label("unit") : t.label("organization");
  return id ? `${word} ${id}` : word;
}

export const scopeOf = (it: { scope: { kind: string; id?: string } }) => (it.scope.id ? `${it.scope.kind}:${it.scope.id}` : it.scope.kind);

/** When something happened: the date, and the time of day for the last few days. */
export function when(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const days = (Date.now() - d.getTime()) / 86_400_000;
  const day = iso.slice(0, 10);
  if (days >= 0 && days < 7) return `${day} ${iso.slice(11, 16)} UTC`;
  return day;
}

export const money = (cents: number) => `$${(cents / 100).toFixed(2)}`;

// ---- loading ----

interface Loaded<T> { key: string; data?: T; error?: unknown }

/** Loads something when the key changes (and again on reload). The old answer stays in view while a reload runs. */
export function useLoad<T>(key: string, load: () => Promise<T>): { data?: T; error?: unknown; loading: boolean; reload: () => void } {
  const [got, setGot] = useState<Loaded<T>>({ key: "" });
  const [n, setN] = useState(0);
  const fn = useRef(load);
  useEffect(() => { fn.current = load; });
  useEffect(() => {
    let live = true;
    fn.current().then((data) => { if (live) setGot({ key, data }); }).catch((error) => { if (live) setGot({ key, error }); });
    return () => { live = false; };
  }, [key, n]);
  const mine = got.key === key;
  return { data: mine ? got.data : undefined, error: mine ? got.error : undefined, loading: !mine, reload: () => setN((x) => x + 1) };
}

export function Loading({ what }: { what?: string }) {
  return <div className="loading" role="status">Loading{what ? " " + what : ""}…</div>;
}

/** What the server said, and what to try next. */
export function ErrorPanel({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (!error) return null;
  const e = error as { message?: string; status?: number; next?: string };
  const status = error instanceof ApiError ? error.status : e.status;
  const next = e.next ?? (status === 404 ? "It may have been removed, or you may not have access to it." : status === 403 ? "Ask someone who maintains this project to give you access." : "Try again in a moment.");
  return (
    <div className="errorbox wk-error" role="alert">
      <strong>{e.message ?? String(error)}</strong>
      <span>{next}</span>
      {onRetry && <Button sm onClick={onRetry}>Try again</Button>}
    </div>
  );
}

/** A designed empty state: one sentence, and the next action as a button or link. */
export function Blank({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="wk-blank">
      <div className="wk-blank-mark" aria-hidden="true"><i /><i /><i /></div>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
      {action && <div className="acts">{action}</div>}
    </div>
  );
}

// ---- pages ----

const AREAS: [string, string][] = [["items", "Items"], ["board", "Board"], ["backlog", "Backlog"], ["sprints", "Sprints"], ["charts", "Charts"], ["roadmap", "Roadmap"], ["milestones", "Milestones"], ["settings", "Settings"]];

/** The links between the pages of one project's work. */
export function ScopeNav({ scope, on }: { scope: string; on: string }) {
  return (
    <nav className="wk-nav" aria-label={`Work in ${scopeLabel(scope)}`}>
      {AREAS.map(([id, label]) => (
        <Link key={id} href={scopeHref(scope, id)} className={on === id ? "on" : undefined}>
          <span aria-current={on === id ? "page" : undefined}>{label}</span>
        </Link>
      ))}
    </nav>
  );
}

/** The frame of a work page: the heading, one sentence, the scope's links. */
export function WorkPage({ title, sentence, eyebrow, scope, on, actions, children, wide }: {
  title: ReactNode; sentence?: ReactNode; eyebrow?: ReactNode; scope?: string; on?: string; actions?: ReactNode; children: ReactNode; wide?: boolean;
}) {
  return (
    <div className={"page wk" + (wide ? " page-wide" : "")}>
      <PageHead title={title} sentence={sentence} eyebrow={eyebrow ?? (scope ? <Link href="/work">Work</Link> : undefined)} actions={actions} />
      {scope && on && <ScopeNav scope={scope} on={on} />}
      {children}
    </div>
  );
}

// ---- small marks ----

const CAT_TONE: Record<string, Tone | undefined> = { todo: undefined, doing: "run", review: "warn", done: "ok", cancelled: undefined };

export function StateTag({ item }: { item: Pick<Item, "state" | "state_name" | "category"> }) {
  return <Chip tone={CAT_TONE[item.category ?? ""]}>{item.state_name ?? item.state.replace(/_/g, " ")}</Chip>;
}

const PRIO_BARS: Record<string, number> = { urgent: 4, high: 3, normal: 2, low: 1 };

/** The priority as bars and a word, so it never rests on color alone. */
export function Prio({ p, bare }: { p: string; bare?: boolean }) {
  const n = PRIO_BARS[p] ?? 2;
  return (
    <span className={`wk-prio p-${p}`} title={`Priority: ${p}`}>
      <span className="bars" aria-hidden="true">{[1, 2, 3, 4].map((i) => <i key={i} className={i <= n ? "on" : ""} />)}</span>
      <span className={bare ? "visually-hidden" : ""}>{p}</span>
    </span>
  );
}

const initials = (name: string) => name.split(/[\s._-]+/).filter(Boolean).slice(0, 2).map((w) => w[0]!.toUpperCase()).join("") || "?";

export function Who({ name, kind }: { name: string; kind?: string }) {
  return (
    <span className="wk-who">
      <span className={"ini" + (kind === "agent" ? " agent" : "")} aria-hidden="true">{initials(name)}</span>
      <span>{name}</span>
      {kind === "agent" && <span className="wk-agent-badge">agent</span>}
    </span>
  );
}

export function Labels({ list }: { list: string[] }) {
  if (!list.length) return null;
  return <ul className="wk-labels" aria-label="Labels">{list.map((l) => <li key={l}>{l}</li>)}</ul>;
}

export function Progress({ done, total, label }: { done: number; total: number; label: string }) {
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div className="wk-prog">
      <div className="track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}><i style={{ width: `${pct}%` }} /></div>
      <span className="num">{done} of {total} ({pct}%)</span>
    </div>
  );
}

export function dueClass(it: Pick<Item, "due_at" | "category">): string {
  return it.due_at && it.category !== "done" && it.category !== "cancelled" && it.due_at < new Date().toISOString() ? "late" : "";
}

// ---- a menu of actions ----

export interface MenuEntry { id: string; label: string; disabled?: boolean; onChoose: () => void }

/** A button that opens a menu. Arrow keys move, Enter chooses, Escape closes and returns to the button: nothing needs a pointer. */
export function ActionMenu({ label, entries, className, ariaLabel }: { label: string; entries: MenuEntry[]; className?: string; ariaLabel?: string }) {
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => { if (!list.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);
  const close = () => { setOpen(false); btn.current?.focus(); };
  const keys = (e: React.KeyboardEvent) => {
    const items = [...(list.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [])];
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); items[(i + 1) % items.length]?.focus(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); items[(i - 1 + items.length) % items.length]?.focus(); }
    else if (e.key === "Home") { e.preventDefault(); items[0]?.focus(); }
    else if (e.key === "End") { e.preventDefault(); items[items.length - 1]?.focus(); }
    else if (e.key === "Tab") setOpen(false);
  };
  return (
    <span className={"wk-menu " + (className ?? "")}>
      <button ref={btn} type="button" className="btn btn-sm btn-ghost" aria-haspopup="menu" aria-label={ariaLabel} aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(!open)}>
        {label}<span aria-hidden="true"> ▾</span>
      </button>
      {open && (
        <div className="wk-menu-list" role="menu" tabIndex={-1} id={id} aria-label={ariaLabel ?? label} ref={list} onKeyDown={keys}>
          {entries.map((e) => (
            <button key={e.id} type="button" role="menuitem" disabled={e.disabled} onClick={() => { setOpen(false); btn.current?.focus(); e.onChoose(); }}>{e.label}</button>
          ))}
        </div>
      )}
    </span>
  );
}

// ---- forms ----

/** A line of text that Markdown can be written in, with a preview that uses the same safe renderer as the page. */
export function Field({ label, children, hint }: { label: string; children: (id: string) => ReactNode; hint?: string }) {
  const id = useId();
  return (
    <div className="wk-f">
      <label htmlFor={id}>{label}</label>
      {children(id)}
      {hint && <span className="hint faint small" id={id + "-h"}>{hint}</span>}
    </div>
  );
}

export const csv = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
