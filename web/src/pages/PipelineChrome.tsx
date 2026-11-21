import { useCallback, type ReactNode } from "react";
import { api } from "../api";
import { EntityLink } from "../components/EntityLink";
import { trendOf } from "../guided/decide";
import { useLoad } from "../guided/useLoad";
import { PageHead } from "../kit/layout";
import { RateText, TrendStrip } from "../kit/Trend";
import { onLink } from "../router";

const PAGES: { id: string; label: string; path: (enc: string) => string }[] = [
  { id: "pipeline", label: "Pipeline", path: (e) => `/projects/${e}/pipeline` },
  { id: "history", label: "Runs", path: (e) => `/projects/${e}/pipeline?view=history` },
  { id: "tests", label: "Tests and coverage", path: (e) => `/projects/${e}/tests` },
  { id: "security", label: "Security", path: (e) => `/projects/${e}/security` },
  { id: "standards", label: "Standing orders", path: (e) => `/projects/${e}/standards` },
  { id: "caches", label: "Build caches", path: (e) => `/projects/${e}/caches` },
  { id: "variables", label: "Variables", path: (e) => `/projects/${e}/variables` },
  { id: "toggles", label: "Toggles", path: (e) => `/projects/${e}/toggles` },
];

/** ProjectHead opens every page about one project's pipeline: the title and sentence, the way across to its
 * sibling pages (pipeline, runs, tests, security, caches), and how its last runs have gone. */
export function ProjectHead({ name, on, title, sentence, actions }: { name: string; on: string; title: ReactNode; sentence?: ReactNode; actions?: ReactNode }) {
  const enc = encodeURIComponent(name);
  const fn = useCallback((bg: boolean) => api.runs({ project: name, limit: 20 }, bg), [name]);
  const runs = useLoad(fn);
  const trend = trendOf(runs.data ?? []);
  return (
    <>
      <PageHead eyebrow={<>Pipelines · <EntityLink kind="project" name={name} /></>} title={title} sentence={sentence} actions={actions} />
      <nav className="pnav" aria-label={`Pages about the pipeline of ${name}`}>
        {PAGES.map((p) => <a key={p.id} href={p.path(enc)} onClick={onLink} aria-current={on === p.id ? "page" : undefined}>{p.label}</a>)}
        <span className="pnav-trend"><TrendStrip trend={trend} label={`The last ${trend.ticks.length} runs of ${name}`} /><RateText trend={trend} /></span>
      </nav>
    </>
  );
}
