import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { Arrange, arranged, BUILTIN, type HomeDoc } from "./panels/Arrange";
import { cleanPanels } from "./panels/registry";
import { Linked } from "../components/EntityLink";
import { LinkButton } from "../kit/layout";
import type { Need } from "../guided/needs";
import { announce } from "../kit/announce";
import { Icon } from "../kit/Icon";
import { Status, type State } from "../kit/Status";
import { ago, elapsed } from "../format";
import { useLoad } from "../guided/useLoad";
import { useNow } from "../live";
import { onLink } from "../router";
import { organizeApi, type AttentionItem, type HomeAnswer, type HomeCategory, type HomeProject, type HomeRunning } from "../organizeApi";
import { terms } from "../terms";
import "./home-sections.css";
import "./panels/panels.css";

// What Home shows once the page has drawn: what needs a person (by the one attention rule), what runs now,
// the projects by Squadron with their health, favorites and what finished lately. One call answers all of it
// (GET /api/v1/home); the page asks again every 10 seconds while it is open. The sections can be hidden,
// reordered and collapsed by each person ("my layout"); someone who may arrange for everyone sets the default.
// See docs/guide/home-page.md and docs/contributing/panels.md.

const TITLES = Object.fromEntries(BUILTIN.map((b) => [b.id, b.title])) as Record<string, string>;
const PanelView = lazy(() => import("./panels/PanelView"));

export default function HomeSections({ onCount, extra = [] }: { onCount?: (n: number) => void; extra?: Need[] }) {
  const [view, setView] = useState<"mine" | "all" | "">("");
  const fn = useCallback((bg: boolean) => organizeApi.home((view || "all") as "mine" | "all", bg), [view]);
  const { data, error, reload } = useLoad<HomeAnswer>(fn, 10000);
  const now = useNow(1000);
  useEffect(() => { if (data) onCount?.(data.needs.length); }, [data, onCount]);
  const effective = (data?.layout.effective ?? {}) as HomeDoc;
  // Where a change is saved: for me (my own layer) or, with the right, for everyone (the Base's shared default).
  const save = useCallback(async (mode: "me" | "everyone", patch: HomeDoc) => {
    if (mode === "me") {
      await organizeApi.setMyLayout("home", { ...((data?.layout.personal ?? {}) as HomeDoc), ...patch } as Record<string, unknown>);
    } else {
      const lvl = data?.layout.levels.find((l) => l.scope === "base");
      await organizeApi.setLayout("home", "base", { ...((lvl?.data ?? {}) as HomeDoc), ...patch } as Record<string, unknown>, lvl?.locked ?? false);
    }
    reload();
  }, [data, reload]);
  if (error && !data) return <p role="alert" className="callout">Home could not be loaded. Try again in a moment.</p>;
  if (!data) return <HomeSkeleton />;
  const panels = cleanPanels(effective.panels);
  const items = arranged(effective, panels);
  const hidden = new Set(effective.hidden ?? []);
  const openCats = effective.open ?? {};
  const effView = view || data.view;
  const toggleCat = (key: string, nowOpen: boolean) => { void save("me", { open: { ...openCats, [key]: !nowOpen } }); };
  const brandNew = data.counts.projects === 0;
  const sharedDoc = (data.layout.shared ?? {}) as HomeDoc;

  return (
    <div className="hm" data-testid="home-sections">
      <div className="hm-top">
        <div className="hm-seg" role="group" aria-label="Whose work to show">
          {(["mine", "all"] as const).map((v) => (
            <button key={v} type="button" className="btn btn-sm" aria-pressed={effView === v} onClick={() => { setView(v); void save("me", { view: v }); }}>{v === "mine" ? "My work" : "Everything I can see"}</button>
          ))}
        </div>
        <Arrange docs={{ me: effective, everyone: sharedDoc }} panelsOf={(d) => cleanPanels(d.panels)} canShare={!!data.layout.may_arrange?.base} customized={data.layout.customized}
          onSave={save} onReset={async () => { await organizeApi.resetMyLayout("home"); setView(""); reload(); announce("Home is back to the default layout."); }} />
      </div>
      {data.layout.customized && <p className="faint small hm-note" role="status">You are viewing your own arrangement of this page.</p>}
      {brandNew && <div className="callout">{`Nothing is here yet. Add a ${terms().lower("project")} and its results, what runs and what needs you appear on this page.`}</div>}
      <div className="hm-grid">
        {items.filter((it) => !hidden.has(it.id)).map((it) => (
          <section key={it.id} className={`hm-sec hm-span-${it.size}`} aria-labelledby={it.custom ? undefined : `hm-${it.id}`}>
            {it.id === "needs" && <Needs data={data} now={now} reload={reload} extra={extra} />}
            {it.id === "running" && <Running data={data} now={now} />}
            {it.id === "favorites" && data.favorites.length > 0 && <Favorites list={data.favorites} now={now} />}
            {it.id === "continue" && <Suspense fallback={<div className="hm-skel" />}><PanelView def={{ id: "continue", type: "continue", title: TITLES.continue, size: 3, settings: {} }} home={data} now={now} /></Suspense>}
            {it.id === "categories" && <Categories cats={data.categories} openCats={openCats} onToggle={toggleCat} now={now} />}
            {it.id === "recent" && <Recent data={data} now={now} />}
            {it.custom && <Suspense fallback={<div className="hm-skel" />}><PanelView def={it.custom} home={data} now={now} /></Suspense>}
          </section>
        ))}
      </div>
    </div>
  );
}

function HomeSkeleton() {
  return <div className="hm" aria-busy="true" aria-label="Loading what needs you and what runs now"><div className="hm-skel" /><div className="hm-skel" /><div className="hm-skel tall" /></div>;
}

const KIND_ICON: Record<AttentionItem["kind"], "x" | "gate" | "alert" | "server"> = { failed: "x", decision: "gate", deployment: "alert", stuck: "server" };

function Needs({ data, now, reload, extra }: { data: HomeAnswer; now: number; reload: () => void; extra: Need[] }) {
  const [showSet, setShowSet] = useState(false);
  const [more, setMore] = useState(false);
  const act = async (fn: () => Promise<unknown>, said: string) => { await fn(); announce(said); reload(); };
  const list = data.needs;
  const total = list.length + extra.length;
  return (
    <div className="sheet">
      <div className="sh-h"><h2 id="hm-needs">Needs you {total > 0 && <span className="vchip warn">{total}</span>}</h2>
        <div className="r">{data.dismissed.length > 0 && <button type="button" className="btn btn-sm" aria-pressed={showSet} onClick={() => setShowSet(!showSet)}>{showSet ? "Hide" : "Show"} set aside ({data.dismissed.length})</button>}</div></div>
      {extra.map((a) => (
        <div key={a.key} className={`need ${a.tone}`}>
          <div className={`ico-tile ${a.tone}`}><Icon name={a.icon} /></div>
          <div className="tx"><div className="ti"><Linked text={a.title} entities={a.entities} /></div><div className="ds"><Linked text={a.detail} entities={a.detailEntities ?? a.entities} /></div></div>
          <div className="acts"><LinkButton href={a.href} sm>{a.button}</LinkButton></div>
        </div>
      ))}
      {list.length === 0 && extra.length === 0 && <p className="sh-b faint">Nothing needs you. A failure that a newer Sortie is already retrying is not listed; it is under Running now.</p>}
      {list.slice(0, more ? list.length : 5).map((n) => <NeedRow key={n.key} n={n} now={now} onSet={(h) => act(() => organizeApi.dismiss(n.key, h), `Set aside${h ? ` for ${h} hours` : ""}.`)} />)}
      {list.length > 5 && <p className="sh-b"><button type="button" className="btn btn-sm" aria-expanded={more} onClick={() => setMore(!more)}>{more ? "Show fewer" : `Show ${list.length - 5} more`}</button></p>}
      {showSet && data.dismissed.map((n) => <NeedRow key={n.key} n={n} now={now} setAside onSet={() => act(() => organizeApi.restore(n.key), "Brought back.")} />)}
    </div>
  );
}

function NeedRow({ n, now, onSet, setAside }: { n: AttentionItem; now: number; onSet: (hours: number) => void; setAside?: boolean }) {
  return (
    <div className={`need ${n.tone}${setAside ? " set-aside" : ""}`}>
      <div className={`ico-tile ${n.tone}`}><Icon name={KIND_ICON[n.kind]} /></div>
      <div className="tx">
        <div className="ti"><a href={n.href} onClick={onLink}>{n.title}</a> <span className="faint small">{ago(n.since, now)}</span></div>
        <div className="ds"><b>Why it is here:</b> {n.why}</div>
        <div className="ds"><b>What to do:</b> {n.do}</div>
      </div>
      <div className="acts hm-acts">
        <a href={n.href} onClick={onLink} className="btn btn-sm btn-primary">Open</a>
        {setAside ? <button type="button" className="btn btn-sm" onClick={() => onSet(0)}>Bring back</button> : <>
          <button type="button" className="btn btn-sm" onClick={() => onSet(24)}>Snooze a day</button>
          <button type="button" className="btn btn-sm" onClick={() => onSet(0)}>Set aside</button></>}
      </div>
    </div>
  );
}

const STATE: Record<string, State> = { running: "run", queued: "idle", succeeded: "ok", failed: "bad" };

function Running({ data, now }: { data: HomeAnswer; now: number }) {
  const list = data.running;
  return (
    <div className="sheet">
      <div className="sh-h"><h2 id="hm-running">Running now {list.length > 0 && <span className="vchip run">{list.length}</span>}</h2></div>
      {list.length === 0 ? <p className="sh-b faint">Nothing is running. Start a Sortie from a pipeline, or push a change.</p> : (
        <ul className="hm-cards" aria-live="off">
          {list.slice(0, 12).map((r) => <RunCard key={r.id} r={r} now={now} />)}
        </ul>
      )}
      {list.length > 12 && <p className="sh-b small faint">and {list.length - 12} more in <a href="/activity?attention=running" onClick={onLink}>Sorties</a>.</p>}
    </div>
  );
}

function RunCard({ r, now }: { r: HomeRunning; now: number }) {
  const where = r.pull_request ? `pull request #${r.pull_request}` : r.branch;
  return (
    <li className={"hm-card run" + (r.mine ? " mine" : "")}>
      <div className="hm-card-h"><Status state={r.waiting ? "wait" : STATE[r.status] ?? "run"} label={r.waiting ? "Waiting for a decision" : r.status === "queued" ? "Queued" : "Running"} />
        <a href={`/runs/${r.id}`} onClick={onLink} className="hm-name">{r.project} #{r.number}</a></div>
      <div className="small">{where}{r.mine && <span className="vchip"> yours</span>}</div>
      <div className="small faint">{r.waiting ? "Waiting for a decision" : r.step ? <>Step <b>{r.step}</b></> : r.status === "queued" ? terms().say("Waiting for a {runner}") : "Starting"} · {elapsed(r.started_at || r.created_at, 0, now) || "just now"}{r.actor ? ` · ${r.actor}` : ""}</div>
      {r.retry_of && <div className="small"><span className="vchip warn">Retrying #{r.retry_of}</span></div>}
    </li>
  );
}

function ProjectCard({ p, now }: { p: HomeProject; now: number }) {
  const state: State = p.needs > 0 ? "bad" : p.last === "failed" ? "bad" : p.last === "succeeded" ? "ok" : p.running > 0 ? "run" : "idle";
  const word = p.needs > 0 ? `${p.needs} need${p.needs === 1 ? "s" : ""} a look` : p.last === "failed" ? "Latest on the default branch failed" : p.last === "succeeded" ? "Latest on the default branch passed" : p.last ? p.last : "No result yet";
  return (
    <li className={"hm-card " + state}>
      <div className="hm-card-h"><Status state={state} label={word} /><a href={`/projects/${encodeURIComponent(p.name)}/pipeline`} onClick={onLink} className="hm-name">{p.name}</a>{p.favorite && <span aria-label="Favorite" title="Favorite">★</span>}</div>
      <div className="small">{word}{p.last_at ? <span className="faint"> · {ago(p.last_at, now)}</span> : null}</div>
      <div className="small faint">{p.running > 0 ? <b>{p.running} running</b> : "Nothing running"}
        {p.history.length > 0 && <span className="trend hm-trend" role="img" aria-label={`Recent results: ${p.history.filter((h) => h === "succeeded").length} passed, ${p.history.filter((h) => h === "failed").length} failed`}>{[...p.history].reverse().map((h, i) => <i key={i} className={h === "succeeded" ? "ok" : h === "failed" ? "bad" : "skip"} />)}</span>}</div>
    </li>
  );
}

function Favorites({ list, now }: { list: HomeProject[]; now: number }) {
  return <div className="sheet"><div className="sh-h"><h2 id="hm-favorites">Favorites</h2></div><ul className="hm-cards">{list.map((p) => <ProjectCard key={p.name} p={p} now={now} />)}</ul></div>;
}

function Categories({ cats, openCats, onToggle, now }: { cats: HomeCategory[]; openCats: Record<string, boolean>; onToggle: (k: string, nowOpen: boolean) => void; now: number }) {
  if (cats.length === 0) return <div className="sheet"><div className="sh-h"><h2 id="hm-categories">By category</h2></div><p className="sh-b faint">{terms().say("No {project} to show in this view.")}</p></div>;
  return (
    <div>
      <div className="sh-h hm-cat-h"><h2 id="hm-categories">By category</h2><a href="/pipelines" onClick={onLink} className="btn btn-sm">Show all in the explorer</a></div>
      {cats.map((c) => {
        // Only the two Squadrons with the most that needs a look open by themselves; the others show one line of health; a person's own choice wins.
        const isOpen = openCats[c.id] ?? (c.needs > 0 && cats.filter((x) => x.needs > 0).indexOf(c) < 2);
        const shut = !isOpen;
        const sorted = [...c.projects].sort((a, b) => b.needs - a.needs || Number(b.last === "failed") - Number(a.last === "failed") || b.running - a.running || a.name.localeCompare(b.name));
        const label = c.kind === "unit" ? `${terms().label("unit")} ${c.label}` : c.label;
        return (
          <div key={c.id} className="hm-cat">
            <h3><button type="button" className="hm-cat-b" aria-expanded={!shut} aria-controls={`cat-${c.id}`} onClick={() => onToggle(c.id, isOpen)}>
              <span aria-hidden="true">{shut ? "▸" : "▾"}</span> {label}
              <span className="faint small"> · {c.projects.length} {c.projects.length === 1 ? terms().say("{project}") : terms().say("{projects}")}</span>
              {c.needs > 0 && <span className="vchip bad">{c.needs} need a look</span>}
              {c.running > 0 && <span className="vchip run">{c.running} running</span>}
              {c.needs === 0 && c.failing === 0 && c.running === 0 && <span className="vchip ok">all passing</span>}
            </button></h3>
            {!shut && <ul id={`cat-${c.id}`} className="hm-cards">{sorted.slice(0, 6).map((p) => <ProjectCard key={p.name} p={p} now={now} />)}
              {sorted.length > 6 && <li className="hm-more"><a href="/pipelines" onClick={onLink}>show all {sorted.length} in the explorer</a></li>}</ul>}
          </div>
        );
      })}
    </div>
  );
}

function Recent({ data, now }: { data: HomeAnswer; now: number }) {
  if (data.recent.length === 0) return null;
  return (
    <div className="sheet hm-recent">
      <div className="sh-h"><h2 id="hm-recent">Recently finished</h2></div>
      <ul className="hm-strip">{data.recent.map((r) => (
        <li key={r.id}><Status state={STATE[r.status] ?? "idle"} /> <a href={`/runs/${r.id}`} onClick={onLink}>{r.project} #{r.number}</a> <span className="faint small">{r.status === "failed" ? "failed" : "passed"} {ago(r.finished_at, now)}</span></li>
      ))}</ul>
    </div>
  );
}
