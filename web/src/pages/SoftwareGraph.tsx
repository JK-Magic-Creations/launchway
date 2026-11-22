/* A table that scrolls sideways is a region the keyboard can reach (axe: scrollable-region-focusable), so its tabindex is deliberate. */
/* eslint-disable jsx-a11y/no-noninteractive-tabindex */
import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "../api";
import { RepoTabs } from "../components/RepoTabs";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Button, PageHead, Sheet } from "../kit/layout";
import "../styles/softgraph.css";
import {
  graphApi, KINDS, KIND_WORDS, refText, sameRef,
  type Advice, type Fact, type GEdge, type GNode, type Impact, type Kind, type Needs, type Order, type Override, type Path, type Placement, type Rebuild, type Ref, type Step, type Summary, type Truncation, type Where,
} from "../softgraph/api";
import { bool, oneOf, useUrlState } from "../urlState";
import { requirementsYaml } from "../softgraph/needsYaml";

const Canvas = lazy(() => import("../softgraph/Canvas"));

const VIEWS = [
  { id: "explore", label: "Explore" }, { id: "impact", label: "What it affects" }, { id: "order", label: "Build order" },
  { id: "needs", label: "What it needs" }, { id: "running", label: "Where it runs" },
] as const;
type View = (typeof VIEWS)[number]["id"];

const EDGE_WORDS: Record<string, string> = {
  "depends-on": "depends on", contains: "contains", "defined-in": "is defined in", builds: "builds", "built-from": "is built from", "instance-of": "is a build of",
  needs: "needs", satisfies: "satisfies", "runs-on": "runs on", hosts: "hosts", "delivered-to": "was delivered to", "configured-by": "is configured by", "owned-by": "is owned by",
  includes: "includes", "order-before": "is built before",
};
const word = (k: string) => EDGE_WORDS[k] ?? k;
const label = (n: { kind: Kind; key: string; name?: string }) => n.name || n.key;
const TRUNC: Record<string, string> = {
  depth: "It stopped at the depth limit: there is more further out.", nodes: "It stopped at the size limit: there is more than is listed.", time: "It stopped at the time limit: the answer is partial.",
};

function Truncated({ t }: { t?: Truncation }) {
  if (!t?.truncated) return null;
  return <p className="sub sg-trunc" role="status"><strong>Not the whole answer.</strong> {TRUNC[t.truncated_reason ?? ""] ?? "It was cut short."}</p>;
}

/** A repository the graph has not read yet answers "no such node": that is not a failure, it says what to do. */
function FocusError({ error, focus }: { error: unknown; focus: string }) {
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  if (!error) return null;
  const repo = focus.startsWith("repository:") ? focus.slice("repository:".length) : "";
  if (!(error instanceof ApiError) || error.status !== 404 || !repo) return <ErrorBox error={error} />;
  const read = () => { setBusy(true); setErr(null); graphApi.refresh(repo).then(() => setDone(true)).catch(setErr).finally(() => setBusy(false)); };
  return (
    <Empty title="The graph has not read this repository yet">
      It is added when the repository is pushed to or a scan runs. You can read it now: Launchway looks at its manifests, Dockerfiles and requirements and adds what it finds.
      {" "}
      {done
        ? <strong role="status">Read. Reload this page to see the result.</strong>
        : <Button kind="pri" disabled={busy} onClick={read}>{busy ? "Reading…" : "Read this repository now"}</Button>}
      <ErrorBox error={err} />
    </Empty>
  );
}

function WhyList({ steps }: { steps?: Step[] }) {
  if (!steps || steps.length === 0) return <p className="faint">This is the thing itself.</p>;
  return (
    <ol className="sg-why">
      {steps.map((s, i) => (
        <li key={i}>
          <Mono>{s.edge.from.kind}:{s.edge.from.key}</Mono> {word(s.edge.kind)} <Mono>{s.edge.to.kind}:{s.edge.to.key}</Mono>
          {(s.edge.constraint || s.edge.resolved) && <> ({s.edge.constraint && <>wants <Mono>{s.edge.constraint}</Mono></>}{s.edge.constraint && s.edge.resolved && ", "}{s.edge.resolved && <>resolved <Mono>{s.edge.resolved}</Mono></>})</>}
          <small className="faint"> {s.edge.confidence}, read by {s.edge.origin}{s.edge.ref ? ` at ${s.edge.ref.slice(0, 9)}` : ""}</small>
        </li>
      ))}
    </ol>
  );
}

// ---- the page ----

/** The graph of one repository: what it contains, what depends on it, what it needs and where it runs. */
export function RepositoryGraphPage({ path }: { path: string }) {
  return (
    <div className="page sg">
      <PageHead eyebrow={<Link href={`/repositories/${path}`}>{path}</Link>} title="Graph"
        sentence="What this repository contains, what it depends on, what depends on it, what is built from it, what it needs and where it runs. Only what you may read is shown." />
      <RepoTabs path={path} on="graph" />
      <Workspace repo={path} initial={`repository:${path}`} />
    </div>
  );
}

/** The graph of everything the person may read: every repository, image, Airfield and machine in their Wing. */
export function GraphPage() {
  const [summary, setSummary] = useState<Summary | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { graphApi.summary().then(setSummary).catch(setError); }, []);
  const total = summary ? Object.values(summary.nodes).reduce((a, b) => a + b, 0) : 0;
  return (
    <div className="page sg">
      <PageHead title="Graph" sentence="One graph from code to what is built from it to what it needs and where it runs. Search for anything you may read, then follow what depends on it, what it affects and where it runs." />
      <ErrorBox error={error} />
      {summary && (
        <p className="sub" role="status">
          {total === 0 ? "The graph is empty: it fills as repositories are pushed and pipelines run." : <>It holds {total} nodes and {summary.edges} relations you may read: {KINDS.filter((k) => summary.nodes[k]).map((k) => `${summary.nodes[k]} ${KIND_WORDS[k].toLowerCase()}${summary.nodes[k] === 1 ? "" : "s"}`).join(", ")}.</>}
        </p>
      )}
      <Workspace />
    </div>
  );
}

function Workspace({ repo, initial }: { repo?: string; initial?: string }) {
  // The view and the focus are in the address (?view=impact&focus=repository:acme/shop), so a link opens the same picture and Back undoes a click.
  const [view, setViewIn] = useUrlState<View>("view", "explore", oneOf(...VIEWS.map((v) => v.id)));
  const [focusIn, setFocusIn] = useUrlState("focus", initial ?? "");
  const focus: string | null = focusIn || null;
  const setView = (v: View) => setViewIn(v, "push");
  const setFocus = (f: string) => setFocusIn(f, "push");
  const focusRef = useMemo<Ref | null>(() => { const i = focus?.indexOf(":") ?? -1; return focus && i > 0 ? { kind: focus.slice(0, i) as Kind, key: focus.slice(i + 1) } : null; }, [focus]);
  const repoPath = repo ?? (focusRef?.kind === "repository" ? focusRef.key : undefined);
  return (
    <>
      {!repo && <Finder onPick={(r) => { setFocus(r); setView("explore"); }} />}
      {focus && (
        <>
          <p className="sub">Focus: <Mono>{focus}</Mono></p>
          <nav className="tabs" aria-label="Views of the graph">
            {VIEWS.map((v) => (
              <button key={v.id} type="button" className={"tab" + (view === v.id ? " tab-on" : "")} aria-current={view === v.id ? "page" : undefined} onClick={() => setView(v.id)}>{v.label}</button>
            ))}
          </nav>
          {view === "explore" && <Explore key={focus} focus={focus} onFocus={setFocus} />}
          {view === "impact" && <ImpactView key={focus} focus={focus} />}
          {view === "order" && <OrderView key={repoPath ?? "all"} repo={repoPath} />}
          {view === "needs" && (repoPath || focusRef?.kind === "component" ? <NeedsView key={focus} focus={focus} repo={repoPath} /> : <p className="faint">What a thing needs is asked of a repository or a component: choose one as the focus.</p>)}
          {view === "running" && <RunningView key={focus} focus={focus} />}
        </>
      )}
      {!focus && <OrderView repo={undefined} />}
    </>
  );
}

// ---- finding a node ----

function Finder({ onPick }: { onPick: (ref: string) => void }) {
  const [text, setText] = useState("");
  const [found, setFound] = useState<{ items: GNode[]; more: boolean } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [kind, setKind] = useState("");
  return (
    <Sheet title="Find something" pad>
      <form className="sg-find" aria-label="Find something in the graph" onSubmit={(e) => { e.preventDefault(); graphApi.search(text, kind || undefined).then((r) => { setFound(r); setError(null); }).catch(setError); }}>
        <label className="field"><span>Name contains</span><input value={text} onChange={(e) => setText(e.target.value)} /></label>
        <label className="field"><span>Kind</span>
          <select value={kind} onChange={(e) => setKind(e.target.value)}><option value="">Any</option>{KINDS.map((k) => <option key={k} value={k}>{KIND_WORDS[k]}</option>)}</select></label>
        <Button kind="pri" type="submit" disabled={text.trim() === ""}>Search</Button>
      </form>
      <ErrorBox error={error} />
      {found && (found.items.length === 0 ? <p className="faint" role="status">Nothing you may read matches.</p> : (
        <>
          <ul className="plain sg-results" aria-label="Search results">
            {found.items.map((n) => (
              <li key={refText(n)}><Mono>{n.kind}:{n.key}</Mono> <Button sm onClick={() => onPick(refText(n))} aria-label={`Show ${n.kind} ${n.key}`}>Show</Button></li>
            ))}
          </ul>
          {found.more && <p className="sub" role="status">There are more matches: narrow the search.</p>}
        </>
      ))}
    </Sheet>
  );
}

// ---- explore ----

interface Known { nodes: Map<string, GNode>; edges: Map<number, GEdge> }

const readAround = (ref: string) => Promise.all([graphApi.neighbours(ref, "downstream"), graphApi.neighbours(ref, "upstream")]);

function useExplore(focus: string) {
  const [known, setKnown] = useState<Known>({ nodes: new Map(), edges: new Map() });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(true); // the first read starts with the page, which is keyed by its focus
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [truncated, setTruncated] = useState<Truncation | undefined>();
  const merge = useCallback((ref: string, [down, up]: Awaited<ReturnType<typeof readAround>>, withNode?: GNode) => {
    setKnown((k) => {
      const nodes = new Map(k.nodes), edges = new Map(k.edges);
      for (const r of [down, up]) {
        const s = r.start ?? r.Start;
        if (s) nodes.set(refText(s), s);
        for (const h of r.hits) {
          nodes.set(refText(h.node), h.node);
          for (const st of h.path ?? []) edges.set(st.edge.id, st.edge);
        }
      }
      if (withNode) nodes.set(refText(withNode), withNode);
      return { nodes, edges };
    });
    setExpanded((e) => new Set(e).add(ref));
    setTruncated(down.truncated ? down : up.truncated ? up : undefined);
    setError(null);
    setBusy(false);
  }, []);
  useEffect(() => {
    let live = true;
    readAround(focus).then((r) => { if (live) merge(focus, r); }).catch((e) => { if (live) { setError(e); setBusy(false); } });
    return () => { live = false; };
  }, [focus, merge]);
  const expand = useCallback((ref: string, withNode?: GNode) => {
    setBusy(true);
    readAround(ref).then((r) => merge(ref, r, withNode)).catch((e) => { setError(e); setBusy(false); });
  }, [merge]);
  return { known, error, busy, expanded, expand, truncated };
}

function relationOf(focus: string, node: GNode, edges: GEdge[]): string {
  const e = edges.find((x) => refText(x.from) === refText(node) || refText(x.to) === refText(node));
  if (!e) return "";
  const nodeIsFrom = refText(e.from) === refText(node);
  const other = nodeIsFrom ? e.to : e.from;
  return nodeIsFrom ? `${word(e.kind)} ${refText(other) === focus ? "the focus" : other.key}` : `${refText(other) === focus ? "the focus" : other.key} ${word(e.kind)} it`;
}

function Explore({ focus, onFocus }: { focus: string; onFocus: (r: string) => void }) {
  const { known, error, busy, expanded, expand, truncated } = useExplore(focus);
  const [kindText, setKindText] = useUrlState("kinds", "");
  const kinds = useMemo(() => new Set(kindText.split(",").filter((k): k is Kind => (KINDS as string[]).includes(k))), [kindText]);
  const setKinds = (f: (s: Set<Kind>) => Set<Kind>) => setKindText([...f(kinds)].join(","));
  const [picture, setPicture] = useUrlState("picture", false, bool);
  const [path, setPath] = useState<Path | null>(null);
  const [pathError, setPathError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | undefined>();
  const all = useMemo(() => [...known.nodes.values()].sort((a, b) => a.kind.localeCompare(b.kind) || a.key.localeCompare(b.key)), [known]);
  const present = useMemo(() => KINDS.filter((k) => all.some((n) => n.kind === k)), [all]);
  const shown = all.filter((n) => kinds.size === 0 || kinds.has(n.kind) || refText(n) === focus);
  const edges = useMemo(() => [...known.edges.values()], [known]);
  const highlight = useMemo(() => new Set(path?.found ? [refText(path.from), refText(path.to), ...(path.steps ?? []).flatMap((s) => [refText(s.edge.from), refText(s.edge.to)])] : []), [path]);
  const lnodes = useMemo(() => shown.map((n) => ({ id: refText(n), kind: n.kind, label: label(n) })), [shown]);
  const ids = useMemo(() => new Set(lnodes.map((n) => n.id)), [lnodes]);
  const ledges = useMemo(() => edges.filter((e) => ids.has(refText(e.from)) && ids.has(refText(e.to))).map((e) => ({ from: refText(e.from), to: refText(e.to), kind: e.kind })), [edges, ids]);
  const showPath = (to: string) => { setPathError(null); graphApi.path(focus, to).then(setPath).catch(setPathError); };
  return (
    <>
      <FocusError error={error} focus={focus} />
      <Sheet title="Filter" pad>
        <fieldset className="sg-kinds">
          <legend>Show only these kinds (none ticked shows all)</legend>
          {present.map((k) => (
            <label key={k} className="check"><input type="checkbox" checked={kinds.has(k)} onChange={() => setKinds((s) => { const n = new Set(s); if (n.has(k)) n.delete(k); else n.add(k); return n; })} /> {KIND_WORDS[k]}</label>
          ))}
        </fieldset>
        <p className="sub" role="status">{busy ? "Reading the graph…" : `${shown.length} of ${all.length} nodes shown. Expand a row to read what is next to it.`}</p>
        <Truncated t={truncated} />
        <Button aria-pressed={picture} onClick={() => setPicture(!picture)}>{picture ? "Hide the picture" : "Show as a picture"}</Button>
      </Sheet>
      {picture && (
        <Sheet title="Picture" pad>
          <Suspense fallback={<p className="faint" role="status">Loading the picture…</p>}>
            <Canvas nodes={lnodes} edges={ledges} focus={focus} highlight={highlight} selected={selected} onSelect={setSelected} />
          </Suspense>
          {selected && <p className="sub" role="status">Selected: <Mono>{selected}</Mono> <Button sm onClick={() => onFocus(selected)}>Make it the focus</Button></p>}
        </Sheet>
      )}
      <Sheet title={`The same things as a table (${shown.length})`}>
        <div className="sg-scroll" tabIndex={0} role="region" aria-label="Table, which scrolls sideways when it is wider than the page"><table className="table sg-table">
          <caption className="sr-only">Nodes around {focus}</caption>
          <thead><tr><th scope="col">Name</th><th scope="col">Kind</th><th scope="col">Next to it</th><th scope="col">Where it came from</th><th scope="col">Actions</th></tr></thead>
          <tbody>
            {shown.map((n) => {
              const r = refText(n);
              return (
                <tr key={r} className={highlight.has(r) ? "on" : undefined} aria-selected={r === selected ? true : undefined}>
                  <th scope="row"><Mono>{n.key}</Mono>{n.name && n.name !== n.key && <><br /><small className="faint">{n.name}</small></>}</th>
                  <td>{KIND_WORDS[n.kind] ?? n.kind}</td>
                  <td>{r === focus ? "The focus" : relationOf(focus, n, edges)}</td>
                  <td><small>{n.confidence ?? ""}{n.origin ? `, read by ${n.origin}` : ""}</small></td>
                  <td className="sg-acts">
                    <Button sm disabled={expanded.has(r) || busy} onClick={() => expand(r, n)} aria-label={`${expanded.has(r) ? "Expanded" : "Expand"} ${n.kind} ${n.key}`}>{expanded.has(r) ? "Expanded" : "Expand"}</Button>
                    {r !== focus && <Button sm onClick={() => onFocus(r)} aria-label={`Make ${n.kind} ${n.key} the focus`}>Focus</Button>}
                    {r !== focus && <Button sm onClick={() => showPath(r)} aria-label={`Show the path from the focus to ${n.kind} ${n.key}`}>Path</Button>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table></div>
      </Sheet>
      <ErrorBox error={pathError} />
      {path && (
        <Sheet title="Path" pad>
          {path.found
            ? <><p className="sub" role="status">{path.way === "affects" ? "A change to the focus reaches it:" : "A change to it reaches the focus:"}</p><WhyList steps={path.steps} /></>
            : <p className="faint" role="status">There is no path between them that you may see.</p>}
        </Sheet>
      )}
    </>
  );
}

// ---- impact ----

function ImpactView({ focus }: { focus: string }) {
  const [impact, setImpact] = useState<Impact | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { graphApi.impact(focus).then(setImpact).catch(setError); }, [focus]);
  return (
    <>
      <FocusError error={error} focus={focus} />
      {!impact && !error && <p className="faint" role="status">Working out what a change reaches…</p>}
      {impact && (
        <>
          <p className="sub" role="status">
            A change to <Mono>{focus}</Mono> reaches {impact.total} {impact.total === 1 ? "thing" : "things"}
            {impact.repositories.length > 0 && <> in {impact.repositories.length} other {impact.repositories.length === 1 ? "repository" : "repositories"}</>}
            {impact.deployments.length > 0 && <>, and {impact.deployments.length} running {impact.deployments.length === 1 ? "deployment" : "deployments"}</>}.
          </p>
          <Truncated t={impact} />
          {impact.deployments.length > 0 && (
            <Sheet title="Running now, affected">
              <div className="sg-scroll" tabIndex={0} role="region" aria-label="Table, which scrolls sideways when it is wider than the page"><table className="table">
                <caption className="sr-only">Deployments that run affected digests</caption>
                <thead><tr><th scope="col">Airfield</th><th scope="col">Deployment</th><th scope="col">Digest</th><th scope="col">Version</th><th scope="col">Why</th></tr></thead>
                <tbody>{impact.deployments.map((d) => (
                  <tr key={d.deployment.key}><td>{d.airfield}</td><th scope="row"><Mono>{d.deployment.key}</Mono></th><td><Mono>{(d.digest ?? "").replace("sha256:", "").slice(0, 12)}</Mono></td><td>{d.version}</td>
                    <td><details><summary>The way a change gets there</summary><WhyList steps={d.why} /></details></td></tr>
                ))}</tbody>
              </table></div>
            </Sheet>
          )}
          {impact.by_owner.map((g) => (
            <Sheet key={g.owner} title={`${g.owner} (${g.count})`}>
              <ul className="plain sg-impact">
                {g.items.slice(0, 200).map((it) => (
                  <li key={refText(it.node)}>
                    <span className="sg-kind">{KIND_WORDS[it.node.kind]}</span> <Mono>{it.node.key}</Mono>
                    <details><summary>Why</summary><WhyList steps={it.path} /></details>
                  </li>
                ))}
              </ul>
              {g.items.length > 200 && <p className="sub">And {g.items.length - 200} more. The command line lists all of them: <Mono>launchway graph impact</Mono>.</p>}
            </Sheet>
          ))}
          {impact.total === 0 && <p className="faint">Nothing you may read depends on it.</p>}
        </>
      )}
    </>
  );
}

// ---- build order ----

function OrderView({ repo }: { repo?: string }) {
  const [order, setOrder] = useState<Order | null>(null);
  const [rebuild, setRebuild] = useState<Rebuild | null>(null);
  const [overrides, setOverrides] = useState<Override[]>([]);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => {
    graphApi.order(repo ? [repo] : [], !!repo).then(setOrder).catch(setError);
    if (repo) graphApi.rebuildSet(repo).then(setRebuild).catch(() => undefined);
    graphApi.overrides().then((r) => setOverrides(r.items ?? [])).catch(setError);
  }, [repo]);
  useEffect(load, [load]);
  const repos = useMemo(() => [...new Set([...(order?.order ?? []), ...(repo ? [repo] : [])])].sort(), [order, repo]);
  return (
    <>
      <ErrorBox error={error} />
      {!order && !error && <p className="faint" role="status">Working out the order…</p>}
      {order && (
        <>
          <Sheet title={repo ? "Build order, with what it depends on" : "Build order of every repository with a dependency"}>
            <p className="sub pad" role="status">Providers are built before the repositories that depend on them; the repositories on one line can be built together.</p>
            {order.levels.length === 0 ? <p className="faint pad">No repository you may read depends on another hosted here.</p> : (
              <ol className="sg-levels" aria-label="Build order">{order.levels.map((l, i) => <li key={i}><strong>Step {i + 1}:</strong> {l.map((r, j) => <span key={r}>{j > 0 && ", "}<Link href={`/repositories/${r}/graph`}>{r}</Link></span>)}</li>)}</ol>
            )}
            <Truncated t={order} />
          </Sheet>
          {(order.cycles ?? []).map((c) => (
            <div key={c.members.join(",")} className="errorbox" role="alert">
              <strong>These repositories depend on each other:</strong> <Mono>{c.path.join(" → ")}</Mono>. {c.suggestion}
            </div>
          ))}
          {rebuild && (
            <Sheet title={`After a push to ${rebuild.repository}`}>
              {rebuild.items.length === 0 ? <p className="faint pad">Nothing else must be built after a push to it.</p> : (
                <ol className="sg-levels pad" aria-label="What must be built again, in order">{rebuild.items.map((it) => (
                  <li key={it.repository}><Link href={`/repositories/${it.repository}/graph`}>{it.repository}</Link>
                    <small className="faint"> after {it.why.map((w) => `${w.provider} (${w.via.join(", ")})`).join(", then ")}</small></li>
                ))}</ol>
              )}
              {(rebuild.skipped ?? []).map((s) => <p key={s.consumer} className="sub pad">Not rebuilt: {s.consumer} is {s.override} ({s.reason}).</p>)}
              <Truncated t={rebuild} />
            </Sheet>
          )}
          <Sheet title={`Why the order is what it is (${order.edges.length})`}>
            {order.edges.length === 0 ? <p className="faint pad">No dependencies.</p> : (
              <div className="sg-scroll" tabIndex={0} role="region" aria-label="Table, which scrolls sideways when it is wider than the page"><table className="table">
                <caption className="sr-only">Dependencies between repositories</caption>
                <thead><tr><th scope="col">Repository</th><th scope="col">Depends on</th><th scope="col">Through</th><th scope="col">Override</th></tr></thead>
                <tbody>{order.edges.map((e) => (
                  <tr key={e.consumer + e.provider}><th scope="row">{e.consumer}</th><td>{e.provider}</td>
                    <td>{e.via.map((v) => <div key={v}><Mono>{v}</Mono></div>)}{e.versions?.length ? <small className="faint">{e.versions.join(", ")}</small> : null}</td>
                    <td>{e.override ? <><strong>{e.override}</strong>: {e.reason}</> : "—"}</td></tr>
                ))}</tbody>
              </table></div>
            )}
          </Sheet>
          <OverridesPanel overrides={overrides} repos={repos} onChange={load} />
        </>
      )}
    </>
  );
}

function OverridesPanel({ overrides, repos, onChange }: { overrides: Override[]; repos: string[]; onChange: () => void }) {
  const [kind, setKind] = useState("ignore");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [value, setValue] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const ready = from !== "" && to !== "" && from !== to && reason.trim() !== "" && (kind !== "pin" || value.trim() !== "");
  const repoSelect = (v: string, set: (s: string) => void, name: string) => (
    <label className="field"><span>{name}</span><select value={v} onChange={(e) => set(e.target.value)}><option value="">Choose a repository</option>{repos.map((r) => <option key={r} value={r}>{r}</option>)}</select></label>
  );
  return (
    <Sheet title="Your corrections to the order" pad>
      <p className="sub">Launchway works the order out from manifests and base images. You can correct it; every correction needs a reason, is shown wherever the order is shown, and can be taken away.</p>
      {overrides.length === 0 ? <p className="faint">No corrections.</p> : (
        <ul className="plain sg-overrides" aria-label="Corrections">
          {overrides.map((o) => (
            <li key={o.id}><strong>{o.kind}</strong> {o.from} → {o.to}{o.value ? ` at ${o.value}` : ""}: {o.reason} <small className="faint">({o.author || "someone"})</small>{" "}
              <Button sm onClick={() => graphApi.removeOverride(o.id).then(onChange).catch(setError)} aria-label={`Remove the correction ${o.kind} ${o.from} to ${o.to}`}>Remove</Button></li>
          ))}
        </ul>
      )}
      <ErrorBox error={error} />
      <form className="sg-find" aria-label="Add a correction" onSubmit={(e) => { e.preventDefault(); graphApi.addOverride({ kind, from, to, value: value || undefined, reason }).then(() => { setReason(""); setError(null); onChange(); }).catch(setError); }}>
        <label className="field"><span>Kind</span>
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="ignore">Ignore: it does not really depend on the other</option>
            <option value="before">Before: build it before the other</option>
            <option value="pin">Pin: keep it at a version, do not rebuild it on a push</option>
          </select></label>
        {repoSelect(from, setFrom, kind === "before" ? "Built first" : "Repository")}
        {repoSelect(to, setTo, kind === "before" ? "Built after it" : "Depends on")}
        {kind === "pin" && <label className="field"><span>Pinned to version</span><input value={value} onChange={(e) => setValue(e.target.value)} /></label>}
        <label className="field"><span>Reason (required)</span><input value={reason} onChange={(e) => setReason(e.target.value)} /></label>
        <Button kind="pri" type="submit" disabled={!ready}>Add the correction</Button>
      </form>
    </Sheet>
  );
}

// ---- needs ----

const SOURCE_WORDS: Record<string, string> = { declared: "Declared in the file", inferred: "Inferred by Launchway" };

function FactsTable({ facts, name }: { facts: Fact[]; name: string }) {
  if (facts.length === 0) return <p className="faint pad">Nothing declared or inferred yet.</p>;
  return (
    <div className="sg-scroll" tabIndex={0} role="region" aria-label="Table, which scrolls sideways when it is wider than the page"><table className="table">
      <caption className="sr-only">What {name} needs</caption>
      <thead><tr><th scope="col">Needs</th><th scope="col">Value</th><th scope="col">Where from</th></tr></thead>
      <tbody>{facts.map((f) => (
        <tr key={f.facet + f.value}><th scope="row">{f.facet}</th><td><Mono>{f.value}</Mono></td>
          <td><span className={"pill " + (f.source === "declared" ? "pill-ok" : "pill-idle")}>{SOURCE_WORDS[f.source] ?? f.source}</span>{f.evidence && <><br /><small className="faint">{f.evidence}</small></>}</td></tr>
      ))}</tbody>
    </table></div>
  );
}

function NeedsView({ focus, repo }: { focus: string; repo?: string }) {
  const [needs, setNeeds] = useState<Needs[] | null>(null);
  const [place, setPlace] = useState<Placement[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [pr, setPr] = useState<{ number: number } | null>(null);
  const [spec, setSpec] = useState<Record<string, unknown> | null>(null);
  const [headroom, setHeadroom] = useState("25");
  useEffect(() => {
    graphApi.needs(focus).then((r) => { setNeeds(r.items ?? []); setText(requirementsYaml(r.items ?? [])); }).catch(setError);
    graphApi.placement(focus).then((r) => setPlace(r.items ?? [])).catch(() => setPlace([]));
  }, [focus]);
  const advice = (needs ?? []).flatMap((n) => n.advice ?? []) as Advice[];
  const specUrl = useMemo(() => (spec ? URL.createObjectURL(new Blob([JSON.stringify(spec, null, 2)], { type: "application/json" })) : ""), [spec]);
  return (
    <>
      <ErrorBox error={error} />
      {!needs && !error && <p className="faint" role="status">Reading what it needs…</p>}
      {needs && needs.length === 0 && <p className="faint" role="status">Nothing is declared or inferred for this yet. Declare what it needs in <Mono>.launchway/requirements.yaml</Mono>.</p>}
      {(needs ?? []).map((n) => (
        <Sheet key={n.component.key} title={<>{n.component.name || n.component.key}</>}>
          <FactsTable facts={n.facts} name={n.component.key} />
        </Sheet>
      ))}
      {advice.length > 0 && (
        <Sheet title="Advice, not applied" pad>
          <p className="sub">What Launchway read differs from what the file declares. The declaration stands; change the file if the advice is right.</p>
          <ul>{advice.map((a) => <li key={a.facet + a.inferred}>{a.message}</li>)}</ul>
        </Sheet>
      )}
      {repo && (
        <Sheet title="Edit the requirements file" pad>
          <p className="sub">The file is <Mono>.launchway/requirements.yaml</Mono> in the repository. Launchway opens a pull request; nothing changes until someone merges it.</p>
          {!editing ? <Button kind="pri" onClick={() => setEditing(true)}>Edit the requirements file</Button> : (
            <form aria-label="Edit the requirements file" onSubmit={(e) => { e.preventDefault(); graphApi.proposeRequirements(repo, text).then((r) => { setPr(r); setError(null); }).catch(setError); }}>
              <label className="field"><span>Requirements file</span><textarea className="mono" rows={16} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} /></label>
              <Button kind="pri" type="submit" disabled={text.trim() === ""}>Open a pull request</Button>{" "}
              <Button onClick={() => { setEditing(false); setText(requirementsYaml(needs ?? [])); }}>Cancel</Button>
            </form>
          )}
          {pr && <p className="sub" role="status">Opened <Link href={`/repositories/${repo}/pulls/${pr.number}`}>pull request #{pr.number}</Link>.</p>}
        </Sheet>
      )}
      {(place ?? []).map((p) => (
        <Sheet key={p.component.key} title={`Where ${p.component.name || p.component.key} can run`} pad>
          <p className="sub" role="status">{p.fits.length} of {p.hosts_considered} machines and platforms satisfy it.</p>
          {p.fits.length > 0 && <ul>{p.fits.map((c) => <li key={c.host.key}><strong>Fits:</strong> <Mono>{c.host.kind}:{c.host.key}</Mono></li>)}</ul>}
          {(p.near_misses ?? []).length > 0 && <ul>{p.near_misses!.map((c) => <li key={c.host.key}><strong>Nearly:</strong> <Mono>{c.host.key}</Mono>, but {(c.missing ?? []).map((m) => m.reason).join("; ")}</li>)}</ul>}
          {p.gaps.map((g) => <p key={g.message} className="errorbox" role="alert"><strong>Gap:</strong> {g.message}</p>)}
        </Sheet>
      ))}
      {needs && needs.length > 0 && (
        <Sheet title="Machine specification" pad>
          <p className="sub">A neutral description of a machine that can run these together: architecture, CPU, memory and storage with headroom, devices, operating-system features, ports and data services. It is an export; nothing is provisioned from it.</p>
          <form className="sg-find" aria-label="Make a machine specification" onSubmit={(e) => { e.preventDefault(); graphApi.specification(needs.map((n) => refText(n.component)), Number(headroom) || undefined).then((s) => { setSpec(s); setError(null); }).catch(setError); }}>
            <label className="field"><span>Headroom, percent</span><input inputMode="numeric" value={headroom} onChange={(e) => setHeadroom(e.target.value.replace(/\D/g, ""))} /></label>
            <Button kind="pri" type="submit">Make the specification</Button>
          </form>
          {spec && (
            <>
              <p><a className="btn" href={specUrl} download="machine-specification.json">Download as JSON</a></p>
              {/* A scrollable region must be reachable by keyboard. */}
              {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
              <pre className="sg-spec" tabIndex={0} aria-label="Machine specification">{JSON.stringify(spec, null, 2)}</pre>
            </>
          )}
        </Sheet>
      )}
    </>
  );
}

// ---- where it runs ----

function RunningView({ focus }: { focus: string }) {
  const [where, setWhere] = useState<Where | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { graphApi.where(focus).then(setWhere).catch(setError); }, [focus]);
  return (
    <>
      <ErrorBox error={error} />
      {!where && !error && <p className="faint" role="status">Looking for where it runs…</p>}
      {where && (where.deployments.length === 0 ? <p className="faint" role="status"><Mono>{focus}</Mono> is not running anywhere you may see.</p> : (
        <Sheet title={`Running in ${where.airfields.length} ${where.airfields.length === 1 ? "Airfield" : "Airfields"}`}>
          <div className="sg-scroll" tabIndex={0} role="region" aria-label="Table, which scrolls sideways when it is wider than the page"><table className="table">
            <caption className="sr-only">Deployments that run {focus}</caption>
            <thead><tr><th scope="col">Airfield</th><th scope="col">Deployment</th><th scope="col">Digest</th><th scope="col">Version</th><th scope="col">How we know</th></tr></thead>
            <tbody>{where.deployments.map((d) => (
              <tr key={d.deployment.key}><td>{d.airfield}</td><th scope="row"><Mono>{d.deployment.key}</Mono></th><td><Mono>{(d.digest ?? "").replace("sha256:", "").slice(0, 12)}</Mono></td><td>{d.version}</td>
                <td>{d.exact ? "It is in the digest that runs." : "Through a dependency or a build: the digest may not contain it."}<details><summary>The way</summary><WhyList steps={d.why} /></details></td></tr>
            ))}</tbody>
          </table></div>
          <Truncated t={where} />
        </Sheet>
      ))}
    </>
  );
}

export { sameRef };
