import { ProjectHead } from "./PipelineChrome";
import { useCallback, useEffect, useMemo, useState } from "react";
import { cacheApi, type CacheEntry, type CacheList, type CacheUse } from "../cacheApi";
import { useMe, canOperate } from "../auth";
import { ago, clock } from "../format";
import { useNow } from "../live";
import { bytes } from "../components/Security";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import "../caches.css";
import { terms } from "../terms";

type Column = "name" | "scope" | "platform" | "bytes" | "last_used_at" | "hits" | "run_number";

interface Sort { col: Column; desc: boolean }

const COLUMNS: { col: Column; label: string; num?: boolean }[] = [
  { col: "name", label: "Cache" },
  { col: "scope", label: "Scope" },
  { col: "platform", label: "Platform" },
  { col: "bytes", label: "Size", num: true },
  { col: "last_used_at", label: "Last used", num: true },
  { col: "hits", label: "Restored", num: true },
  { col: "run_number", label: "Written by", num: true },
];

function compare(a: CacheEntry, b: CacheEntry, col: Column): number {
  const x = a[col], y = b[col];
  return typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
}

/** The first characters of a key, enough to tell entries apart. */
function shortKey(key: string): string { return key.slice(0, 12); }

/** Whole GiB without a decimal point, small ones with up to three digits. */
function gib(n: number): string {
  return `${Number(n.toFixed(n >= 10 ? 0 : 3))} GiB`;
}

// ProjectCachesPage lists a project's build caches against its quota, and
// lets those who may operate the project end an entry, or every entry of a
// cache. The server decides who may; the buttons are only hidden from
// people who would be refused.
/** The empty page says why it is empty: no step asks for a cache (with the lines to add), or none has run since. */
function NothingCached({ use, project }: { use?: CacheUse; project: string }) {
  const none = !!use && use.run > 0 && use.steps.length === 0;
  const layers = use?.layers ?? [];
  const where = layers.length > 0 ? (
    <p className="faint small">
      The image step{layers.length === 1 ? "" : "s"} {layers.map((l) => l.step).join(", ")} keep{layers.length === 1 ? "s" : ""} {terms().say("build layers in the {registry} (")}{layers.map((l) => <Mono key={l.step}>{l.registry}/{l.repository}</Mono>)}{terms().say("), apart from this list: the {registry} holds them, and the Images pages show what it holds.")}{" "}
      <Link href={`/projects/${encodeURIComponent(project)}/pipeline`}>See the pipeline</Link>.
    </p>
  ) : null;
  if (none) {
    return (
      <Empty title="No step in this pipeline uses a cache">
        {terms().say("Nothing is kept between runs, so every {run} downloads its dependencies again. To keep a directory, give a step a")} <Mono>cache</Mono>, with a key made of the lock files and the toolchain, so that a changed dependency starts a new one:
        <pre className="code">{`cache:
  - name: go
    paths: [.cache/go-mod, .cache/go-build]
    key: { files: ["**/go.sum"], text: go-1.27 }
    fallback: latest
    max_age: 7d`}</pre>
        {where}
      </Empty>
    );
  }
  return (
    <Empty title="Nothing is cached yet">
      A step keeps a directory between runs with a <Mono>cache</Mono> {terms().say("key; the first {run} saves it and the next restores it.")}
      {use && use.steps.length > 0 && <> Steps that ask for one: {use.steps.map((s) => <Mono key={s.step}>{s.step} ({s.caches.join(", ")})</Mono>)}.</>}
      <pre className="code">{`cache:
  - name: pip
    paths: [.cache/pip]
    key: { files: [requirements.txt] }`}</pre>
      {where}
    </Empty>
  );
}

export function ProjectCachesPage({ name }: { name: string }) {
  const me = useMe();
  const mayDelete = canOperate(me, name);
  const [list, setList] = useState<CacheList | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const [sort, setSort] = useState<Sort>({ col: "last_used_at", desc: true });
  const now = useNow(5000);

  const load = useCallback((background: boolean) => {
    cacheApi.list(name, background).then((l) => { setList(l); setError(null); }).catch(setError);
  }, [name]);
  useEffect(() => {
    load(false);
    const t = setInterval(() => load(true), 5000);
    return () => clearInterval(t);
  }, [load]);

  const rows = useMemo(() => {
    const out = [...(list?.entries ?? [])];
    out.sort((a, b) => (sort.desc ? -1 : 1) * compare(a, b, sort.col) || a.id.localeCompare(b.id));
    return out;
  }, [list, sort]);
  const names = useMemo(() => {
    const by = new Map<string, { entries: number; bytes: number }>();
    for (const e of list?.entries ?? []) {
      const n = by.get(e.name) ?? { entries: 0, bytes: 0 };
      n.entries++; n.bytes += e.bytes;
      by.set(e.name, n);
    }
    return [...by.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [list]);

  const act = async (what: () => Promise<{ ended: number; bytes: number }>) => {
    setBusy(true); setError(null); setSaid("");
    try {
      const r = await what();
      setSaid(terms().say(`Ended ${r.ended === 1 ? "1 entry" : `${r.ended} entries`}, ${bytes(r.bytes)}. The next {run} that wanted ${r.ended === 1 ? "it" : "them"} starts without and saves again.`));
      load(true);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const deleteEntry = (e: CacheEntry) => {
    if (!window.confirm(terms().say(`End the ${e.name} entry ${shortKey(e.key)}… (${e.scope}, ${bytes(e.bytes)})?\n\nThe next {run} that wanted it starts without it, and saves it again.`))) return;
    act(() => cacheApi.deleteEntry(name, e.id));
  };
  const deleteName = (n: string, count: number) => {
    if (!window.confirm(terms().say(`End all ${count} ${count === 1 ? "entry" : "entries"} of the cache ${n}, in every scope and key?\n\nThe next {run} that wanted one starts without it, and saves it again.`))) return;
    act(() => cacheApi.deleteCachesNamed(name, n));
  };
  const sortBy = (col: Column) => setSort((s) => (s.col === col ? { col, desc: !s.desc } : { col, desc: col !== "name" && col !== "scope" && col !== "platform" }));

  const t = list?.totals;
  const share = t && t.quota_bytes > 0 ? t.bytes / t.quota_bytes : 0;
  return (
    <div className="page">
      <ProjectHead name={name} on="caches" title={<>Caches of {name}</>}
        sentence={<>What its steps keep between runs (the <Mono>cache</Mono> key of a step). A pull request restores the default branch's entries and saves into its own scope only.</>} />
      <ErrorBox error={error} />
      {said && <div className="callout callout-info" role="status">{said}</div>}

      {list && !list.policy.enabled && (
        <div className="callout callout-draft" role="status">
          <strong>{terms().say("Caching is turned off for this {project}.")}</strong> Every lookup misses and nothing is saved
          ({list.policy.source === "project" ? terms().say("this {project}'s own cache policy") : `the ${terms().lower("installation")}'s cache policy`}); steps run as without
          a <Mono>cache</Mono> key. An administrator changes this with <Mono>launchway caches policy</Mono> or the API.
          {list.entries.length > 0 && " Entries kept before are not restored, and end after their idle days."}
        </div>
      )}

      {list === null && !error && <div className="loading">Loading…</div>}
      {list && t && (
        <section className="card" aria-label="Quota">
          <div className="cc-use">
            <span><strong>{bytes(t.bytes)}</strong> of {bytes(t.quota_bytes)}</span>
            <span>{t.entries === 1 ? "1 entry" : `${t.entries} entries`}</span>
            <span className="faint">An entry may hold {bytes(t.max_entry_bytes)} · ended after {t.idle_days} {t.idle_days === 1 ? "day" : "days"} without being restored</span>
          </div>
          <div className="store-meter cc-meter" role="meter" aria-label="Cache quota used" aria-valuemin={0} aria-valuemax={t.quota_bytes} aria-valuenow={t.bytes}
            aria-valuetext={`${bytes(t.bytes)} of ${bytes(t.quota_bytes)}`}>
            <span className={share >= 0.8 ? "store-meter-fill store-meter-high" : "store-meter-fill"} style={{ width: `${Math.min(100, Math.max(share * 100, t.bytes > 0 ? 1 : 0))}%` }} />
          </div>
          <p className="faint small">
            Quota {gib(list.policy.max_project_gib)} · set {list.policy.source === "project" ? terms().say("for this {project}") : list.policy.source === "installation" ? `for the ${terms().lower("installation")}` : "by default"}.
            When a save would pass it, the least recently used entries are ended to make room.
          </p>
        </section>
      )}

      {list && list.entries.length === 0 && <NothingCached use={list.use} project={name} />}

      {names.length > 0 && (
        <>
          <h2 className="h-sm cc-h">By cache</h2>
          <section className="card">
            <ul className="cc-names">
              {names.map(([n, v]) => (
                <li key={n}>
                  <span><span className="cc-name">{n}</span> <span className="faint">{v.entries === 1 ? "1 entry" : `${v.entries} entries`}, {bytes(v.bytes)}</span></span>
                  {mayDelete && (
                    <button type="button" className="btn btn-danger-outline btn-sm" disabled={busy} onClick={() => deleteName(n, v.entries)}
                      aria-label={`Delete every entry of the cache ${n}`}>Delete all</button>
                  )}
                </li>
              ))}
            </ul>
          </section>
        </>
      )}

      {rows.length > 0 && (
        <>
          <h2 className="h-sm cc-h">Entries</h2>
          <div className="card card-flush table-scroll">
            <table className="table cc-table">
              <thead>
                <tr>
                  {COLUMNS.map((c) => (
                    <th key={c.col} className={c.num ? "cc-num" : undefined} aria-sort={sort.col === c.col ? (sort.desc ? "descending" : "ascending") : "none"}>
                      <button type="button" className="cc-sort" onClick={() => sortBy(c.col)}>
                        {c.label}
                        <span className="cc-arrow" aria-hidden="true">{sort.col === c.col ? (sort.desc ? "▼" : "▲") : ""}</span>
                      </button>
                      {c.col === "name" && <span className="faint small"> and key</span>}
                    </th>
                  ))}
                  {mayDelete && <th><span className="visually-hidden">Actions</span></th>}
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <tr key={e.id}>
                    <td>
                      <strong>{e.name}</strong>{e.status === "uploading" && <span className="faint"> (being saved)</span>}
                      <div className="cc-key" title={e.key}>{shortKey(e.key)}…{e.generation > 1 ? `, generation ${e.generation}` : ""}</div>
                      <div className="faint small">{e.pipeline || "an older entry, no longer restored"}{e.sensitive ? ", sensitive" : ""}</div>
                    </td>
                    <td className="cc-scope">{e.scope}</td>
                    <td>{e.platform}</td>
                    <td className="cc-num" title={`${e.bytes.toLocaleString()} bytes in ${e.files.toLocaleString()} files`}>{bytes(e.bytes)}</td>
                    <td className="cc-num" title={clock(e.last_used_at)}>{ago(e.last_used_at, now)}</td>
                    <td className="cc-num">{e.hits === 1 ? "once" : `${e.hits} times`}</td>
                    <td className="cc-num">{e.run_id ? <Link href={`/runs/${e.run_id}`}>#{e.run_number || "?"}</Link> : <span className="faint">unknown</span>}</td>
                    {mayDelete && (
                      <td className="td-actions">
                        <button type="button" className="btn btn-danger-outline btn-sm" disabled={busy} onClick={() => deleteEntry(e)}
                          aria-label={`Delete the ${e.name} entry ${shortKey(e.key)}`}>Delete</button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!mayDelete && <p className="faint small">{terms().say("Deleting entries needs the operator role on this {project}.")}</p>}
        </>
      )}
    </div>
  );
}
