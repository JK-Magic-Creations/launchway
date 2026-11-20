import { useCallback, useEffect, useState } from "react";
import { configApi, kindLabel, kindOf, levelOf, useOverview, type ConfigObject } from "../configApi";
import { ago } from "../format";
import { navigate, usePath } from "../router";
import { ConfigNav, wizardFor } from "../components/ConfigNav";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { levelWords } from "../configKinds";
import "../config.css";

// ConfigListPage lists the objects of one kind, with where each is defined,
// who manages it, and the deleted ones on request (to restore them).
export function ConfigListPage({ kind }: { kind: string }) {
  const path = usePath();
  const { overview, error: ovError } = useOverview();
  const k = kindOf(overview, kind);
  const [objects, setObjects] = useState<ConfigObject[] | null>(null);
  const [deleted, setDeleted] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState("");

  const load = useCallback(() => {
    if (!k) return;
    configApi.list(k.path, deleted).then((r) => { setObjects(r.objects); setError(null); }).catch(setError);
  }, [k, deleted]);
  useEffect(load, [load]);
  // A singleton has one page: its object.
  useEffect(() => { if (k?.singleton) navigate(`/config/${k.path}/${k.singleton}`); }, [k]);

  if (ovError) return <div className="page"><ErrorBox error={ovError} /></div>;
  if (!overview) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!k) return <div className="page"><ConfigNav overview={overview} path={path} /><Empty title="No such kind of configuration">There is no {kind}. <Link href="/config">Back to configuration</Link>.</Empty></div>;

  const label = kindLabel(overview, k);
  const one = kindLabel(overview, k, false);
  const wiz = wizardFor(k.name);
  const showLevel = k.levels && overview.units_in_use;
  const restore = async (o: ConfigObject) => {
    if (!window.confirm(`Restore ${one.toLowerCase()} ${o.name}? It applies again at once, as it was when it was deleted.`)) return;
    setBusy(o.name);
    try { await configApi.restore(k.path, o.name, o.version); load(); } catch (e) { setError(e); } finally { setBusy(""); }
  };
  return (
    <div className="page cf-page">
      <ConfigNav overview={overview} path={path} />
      <header className="page-head">
        <div>
          <h1>{label}</h1>
          <p className="sub">{levelWords(k.doc, overview.levels)}</p>
        </div>
        <div className="cf-head-actions">
          {wiz && <Link href={`/config/wizard/${wiz.id}`} className="btn btn-primary">{wiz.title}</Link>}
          <Link href={`/config/${k.path}/new`} className={"btn" + (wiz ? "" : " btn-primary")}>{wiz ? "Add with a plain form" : `Add a ${one.toLowerCase()}`}</Link>
        </div>
      </header>
      <ErrorBox error={error} />
      <label className="checkline"><input type="checkbox" checked={deleted} onChange={(e) => setDeleted(e.target.checked)} /> Show deleted ones too</label>
      {objects === null && !error && <div className="loading">Loading…</div>}
      {objects && objects.length === 0 && (
        <Empty title={`No ${label.toLowerCase()} yet`}>
          {wiz ? <><Link href={`/config/wizard/${wiz.id}`}>{wiz.title}</Link> with a few questions, or <Link href={`/config/${k.path}/new`}>fill in a form</Link>.</>
            : <Link href={`/config/${k.path}/new`}>Add one</Link>}
        </Empty>
      )}
      {objects && objects.length > 0 && (
        <ul className="cf-objlist">
          {objects.map((o) => (
            <li key={o.name} className={"cf-obj" + (o.deleted ? " cf-obj-deleted" : "")}>
              <div className="cf-obj-main">
                <Link href={`/config/${k.path}/${encodeURIComponent(o.name)}`} className="cf-obj-name"><Mono>{o.name}</Mono></Link>
                {o.deleted && <span className="cf-pill cf-pill-bad">deleted</span>}
                {o.managed_by === "terraform" && <span className="cf-pill cf-pill-warn" title="Changes here may be overwritten by the next Terraform apply">managed by Terraform</span>}
                {o.managed_by === "import" && <span className="cf-pill">imported</span>}
                <div className="cf-obj-sum">{summary(k.name, o)}</div>
              </div>
              <div className="cf-obj-meta">
                {showLevel && <span>{levelOf(overview, o.unit)}</span>}
                <span>version {o.version}</span>
                <span>{o.deleted ? `deleted ${ago(o.deleted_at ?? 0)}${o.deleted_by ? ` by ${o.deleted_by}` : ""}` : `changed ${ago(o.updated_at)}${o.updated_by ? ` by ${o.updated_by}` : ""}`}</span>
                {o.deleted && <button type="button" className="btn btn-sm" disabled={busy === o.name} onClick={() => restore(o)}>Restore</button>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** A line about an object, from its spec. */
function summary(kind: string, o: ConfigObject): string {
  const s = o.spec as Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any
  const list = (l?: string[]) => (l?.length ? l.join(", ") : "");
  switch (kind) {
    case "project": return [s.repo, s.branch && `branch ${s.branch}`, s.pipeline?.template && `template ${s.pipeline.library}/${s.pipeline.template}`].filter(Boolean).join(" · ");
    case "library": return s.repo ?? "";
    case "environment": return [`target ${s.target ?? "?"}`, s.projects?.length && `projects ${list(s.projects)}`, s.protected && "protected"].filter(Boolean).join(" · ");
    case "runner-pool": return s.kind === "build" ? `build pool for ${list(s.projects) || "no projects"}` : `deploy pool for ${list(s.targets) || "no targets"}`;
    case "role-binding": return `${s.group ? "group " + s.group : "user " + (s.user ?? "?")} is ${s.role ?? "?"}${s.projects?.length ? " on " + list(s.projects) : " everywhere"}`;
    case "policy": return [s.mode, Object.keys(s.rules ?? {}).join(", ")].filter(Boolean).join(" · ");
    case "policy-source": return `${s.library ?? "?"}@${s.version ?? "?"}`;
    case "health-source": return [s.type, s.projects?.length && `for ${list(s.projects)}`].filter(Boolean).join(" · ");
    case "model": return [s.provider, s.model, s.projects?.length && `for ${list(s.projects)}`].filter(Boolean).join(" · ");
    case "secret-store": return [s.type, s.description].filter(Boolean).join(" · ");
    case "unit": return [s.display_name, s.description].filter(Boolean).join(" · ");
    default: return "";
  }
}
