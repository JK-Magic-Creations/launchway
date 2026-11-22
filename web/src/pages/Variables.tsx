import { useEffect, useState } from "react";
import { api } from "../api";
import { loadOverview, type UnitView } from "../configApi";
import { VariablesPanel } from "../components/Variables";
import { PageHead, Sheet } from "../kit/layout";
import { useTerms, terms } from "../terms";
import { VAR_LEVELS, type VarLevel } from "../variablesApi";
import { ProjectHead } from "./PipelineChrome";

/** Settings → Variables: the variables at one level, with what each inherits from above. */
export function SettingsVariablesPage() {
  const t = useTerms();
  const [level, setLevel] = useState<VarLevel>("installation");
  const [scope, setScope] = useState("");
  const [units, setUnits] = useState<UnitView[]>([]);
  const [projects, setProjects] = useState<string[]>([]);
  useEffect(() => {
    loadOverview().then((o) => setUnits(o.units ?? [])).catch(() => undefined);
    api.projects().then((ps) => setProjects(ps.map((p) => p.name))).catch(() => undefined);
  }, []);
  const choices = level === "unit" ? units.map((u) => u.name) : level === "project" ? projects : [];
  const chosen = choices.includes(scope) ? scope : (choices[0] ?? "");
  const word = (l: VarLevel) => t.label(l);
  const needsScope = level === "unit" || level === "project";
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="Variables" sentence="Named settings you set once at a level. Everything below gets them; a nearer level can override one, and a lock keeps it from being overridden." />
      <Sheet pad>
        <div className="vr-tabs" role="tablist" aria-label="Level">
          {VAR_LEVELS.map((l) => (
            <a key={l} href={`#${l}`} role="tab" aria-selected={level === l} aria-current={level === l ? "page" : undefined}
              onClick={(e) => { e.preventDefault(); setLevel(l); }}>{word(l)}</a>
          ))}
          {needsScope && (
            <select aria-label={`Which ${word(level).toLowerCase()}`} value={chosen} onChange={(e) => setScope(e.target.value)}>
              {choices.length === 0 && <option value="">None yet</option>}
              {choices.map((c) => <option key={c} value={c}>{c}</option>)}
            </select>
          )}
        </div>
        {needsScope && choices.length === 0
          ? <p className="sub">There is no {word(level).toLowerCase()} yet. {level === "unit" ? "Create one in Configuration." : terms().say("Add a {project} first.")}</p>
          : <VariablesPanel key={level + ":" + chosen} level={level} scope={needsScope ? chosen : undefined} />}
      </Sheet>
    </div>
  );
}

/** A project's variables: what is in effect for it, where each comes from, and what it overrides. */
export function ProjectVariablesPage({ name }: { name: string }) {
  const [env, setEnv] = useState("");
  const [applied, setApplied] = useState("");
  return (
    <div className="page">
      <ProjectHead name={name} on="variables" title={`Variables of ${name}`}
        sentence={terms().say("Everything that reaches this {project} from above, and what it sets itself. Set a variable here to override the one from a wider level, unless a lock stops it.")} />
      <Sheet pad>
        <form className="vr-tabs" onSubmit={(e) => { e.preventDefault(); setApplied(env.trim()); }}>
          <label>{terms().say("For an {environment}")} <input value={env} onChange={(e) => setEnv(e.target.value)} placeholder="all (general)" aria-label={terms().say("{Environment}")} /></label>
          <button className="btn btn-sm" type="submit">Show</button>
        </form>
        <VariablesPanel key={applied} level="project" scope={name} environment={applied || undefined} />
      </Sheet>
    </div>
  );
}

