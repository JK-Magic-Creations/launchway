import { ErrorBoundary } from "../../components/ErrorBoundary";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { announce } from "../../kit/announce";
import { ago } from "../../format";
import { forgeApi, type HostedRepo } from "../../forgeApi";
import { ErrorBox, Link } from "../../components/ui";
import { PageHead } from "../../kit/layout";
import { codeHref, type RefInfo, browseApi } from "../../browseApi";
import { onLink } from "../../router";
import { RepoTabs, type RepoTabId } from "../../components/RepoTabs";
import { ActionPicture, type ActionIcon } from "./icons";
import "../../styles/code.css";

/** useQuery is the address's query, re-read when the address changes. */
export function useQuery(): URLSearchParams {
  const [q, setQ] = useState(() => new URLSearchParams(location.search));
  useEffect(() => {
    const on = () => setQ(new URLSearchParams(location.search));
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  return q;
}

/** useFetched loads something that depends on a key (the page's address): what shows is only ever the answer for the
 * current key, so a stale answer never appears under a new address. fn is null when there is nothing to ask. */
export function useFetched<T>(key: string, fn: (() => Promise<T>) | null): { data: T | null; error: unknown; set: (v: T) => void } {
  const [st, setSt] = useState<{ k: string; v?: T; e?: unknown } | null>(null);
  const run = useRef(fn);
  const has = fn !== null;
  useEffect(() => { run.current = fn; });
  useEffect(() => {
    const f = run.current;
    if (!f) return;
    let alive = true;
    f().then((v) => alive && setSt({ k: key, v })).catch((e) => alive && setSt({ k: key, e }));
    return () => { alive = false; };
  }, [key, has]);
  const mine = has && st?.k === key ? st : null;
  return { data: mine && !mine.e ? (mine.v ?? null) : null, error: mine?.e ?? null, set: (v: T) => setSt({ k: key, v }) };
}

/** useRepo loads the repository itself: its default branch and clone addresses. */
export function useRepo(path: string): { repo: HostedRepo | null; error: unknown } {
  const [repo, setRepo] = useState<HostedRepo | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const [o, ...n] = path.split("/");
    forgeApi.repository(o, n.join("/")).then(setRepo).catch(setError);
  }, [path]);
  return { repo, error };
}

/** The age of a time written as text, or its date when it is older than a month. */
export function when(at: string, now = Date.now()): string {
  const t = Date.parse(at);
  if (!t) return "";
  return now - t > 30 * 86400_000 ? new Date(t).toISOString().slice(0, 10) : ago(t, now);
}

export function useCopy(): (text: string, what: string) => void {
  return useCallback((text: string, what: string) => {
    const done = () => announce(`Copied ${what}`);
    if (navigator.clipboard?.writeText) navigator.clipboard.writeText(text).then(done, () => announce(`Could not copy ${what}: select it and copy by hand`, "assertive"));
    else announce(`Could not copy ${what}: select it and copy by hand`, "assertive");
  }, []);
}

export function CopyButton({ text, what, label, icon }: { text: string; what: string; label?: string; icon?: ActionIcon }) {
  const copy = useCopy();
  if (icon) return <button type="button" className="btn btn-sm btn-icon" onClick={() => copy(text, what)} aria-label={`Copy ${what}`} title={label ?? `Copy ${what}`}><ActionPicture name={icon} /></button>;
  return <button type="button" className="btn btn-sm" onClick={() => copy(text, what)} aria-label={`Copy ${what}`}>{label ?? "Copy"}</button>;
}

/** A commit's message as a link: normal weight, a subdued color, one line cut with an ellipsis; the whole message is
 * the tooltip and, for someone using the keyboard, shows in full while the link has focus. */
export function MsgLink({ href, text }: { href: string; text: string }) {
  return <a href={href} onClick={onLink} className="msg-link" title={text}>{text}</a>;
}

/** A button or link that is only a picture: its name is for assistive technology and for the hover tooltip. */
export function IconAction({ icon, label, href, download, onClick }: { icon: ActionIcon; label: string; href?: string; download?: string | boolean; onClick?: () => void }) {
  const inner = <ActionPicture name={icon} />;
  if (href !== undefined && download) return <a className="btn btn-sm btn-icon" href={href} download={download === true ? "" : download} aria-label={label} title={label}>{inner}</a>;
  if (href !== undefined) return <a href={href} onClick={onLink} className="btn btn-sm btn-icon" aria-label={label} title={label}>{inner}</a>;
  return <button type="button" className="btn btn-sm btn-icon" onClick={onClick} aria-label={label} title={label}>{inner}</button>;
}

export type RepoTab = RepoTabId;

/** The frame every page of a repository's code has: its name, the tabs, and what to say about it. */
export function RepoFrame({ path, tab, title, sentence, actions, error, children }: {
  path: string; tab: RepoTab; title: ReactNode; sentence?: ReactNode; actions?: ReactNode; error?: unknown; children: ReactNode;
}) {
  return (
    <div className="page code-page">
      <PageHead eyebrow={path} title={title} sentence={sentence} actions={actions} />
      <RepoTabs path={path} on={tab} />
      <ErrorBox error={error} />
      <ErrorBoundary name="This part of the repository page" resetKey={path + tab}>{children}</ErrorBoundary>
    </div>
  );
}

/** A path as links: the repository, then each folder. */
export function Breadcrumb({ repo, refName, path }: { repo: string; refName: string; path: string }) {
  const parts = path ? path.split("/") : [];
  return (
    <nav aria-label="Path" className="crumbs-code">
      <ol>
        <li><Link href={codeHref(repo, refName)}>{repo.split("/").slice(1).join("/")}</Link></li>
        {parts.map((p, i) => {
          const to = parts.slice(0, i + 1).join("/");
          return <li key={to}>{i === parts.length - 1 ? <span aria-current="page">{p}</span> : <Link href={codeHref(repo, refName, to)}>{p}</Link>}</li>;
        })}
      </ol>
    </nav>
  );
}

/** RefSwitcher: the current branch, tag or commit, and a box to find another by typing. It is a combobox: the
 * arrows move through the matches, Enter picks, Escape closes. Its button never changes size with the name. */
export function RefSwitcher({ repo, current, onPick, label = "Switch branch or tag" }: { repo: string; current: string; onPick: (ref: string) => void; label?: string }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<"branch" | "tag">("branch");
  const [items, setItems] = useState<RefInfo[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [at, setAt] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => {
      (kind === "branch" ? browseApi.branches(repo, q, 50) : browseApi.tags(repo, q, 50)).then((r) => { setItems(r.items); setAt(0); setError(null); }).catch(setError);
    }, q ? 120 : 0);
    return () => clearTimeout(t);
  }, [open, q, kind, repo]);
  useEffect(() => { if (open) input.current?.focus(); }, [open]);
  const close = () => { setOpen(false); setQ(""); button.current?.focus(); };
  const pick = (r: string) => { setOpen(false); setQ(""); onPick(r); };
  const exact = /^[0-9a-f]{7,40}$/.test(q.trim()) ? q.trim() : "";
  const list = [...(items ?? []).map((i) => i.name), ...(exact ? [exact] : [])];
  const key = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); setAt((a) => Math.min(a + 1, list.length - 1)); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setAt((a) => Math.max(a - 1, 0)); }
    else if (e.key === "Enter" && list[at]) { e.preventDefault(); pick(list[at]); }
  };
  return (
    <div className="refsw">
      <button type="button" className="btn refsw-btn" ref={button} aria-haspopup="dialog" aria-expanded={open} aria-label={`${label}: ${current}`} onClick={() => setOpen(!open)}>
        <span className="refsw-name mono">{current.length === 40 ? current.slice(0, 10) : current}</span><span aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="refsw-pop" role="dialog" aria-label={label}>
          <div className="seg" role="group" aria-label="Kind">
            {(["branch", "tag"] as const).map((k) => <button key={k} type="button" className={"btn btn-sm" + (kind === k ? " btn-on" : "")} aria-pressed={kind === k} onClick={() => setKind(k)}>{k === "branch" ? "Branches" : "Tags"}</button>)}
          </div>
          <input ref={input} role="combobox" aria-expanded="true" aria-controls="refsw-list" aria-autocomplete="list" aria-activedescendant={list[at] ? `refsw-${at}` : undefined}
            aria-label="Find a branch, tag or commit" placeholder="Find a branch, tag or commit" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={key} />
          <ErrorBox error={error} />
          <ul id="refsw-list" role="listbox" aria-label="Matches">
            {list.map((n, i) => (
              <li key={n} id={`refsw-${i}`} role="option" aria-selected={i === at} className={i === at ? "on" : ""} onMouseDown={(e) => { e.preventDefault(); pick(n); }}>
                <span className="mono">{n.length === 40 ? n.slice(0, 10) : n}</span>{n === current && <span className="faint small"> current</span>}
                {i >= (items?.length ?? 0) && <span className="faint small"> commit</span>}
              </li>
            ))}
            {items && list.length === 0 && <li className="faint small" role="presentation">Nothing is called that. To open a commit, type at least 7 of its characters.</li>}
          </ul>
        </div>
      )}
    </div>
  );
}
