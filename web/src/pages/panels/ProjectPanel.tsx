import { ago } from "../../format";
import { onLink } from "../../router";
import { Status, type State } from "../../kit/Status";
import type { PanelProps } from "./registry";
import { terms } from "../../terms";

/** One project's status, from the answer Home was drawn from: its latest result, what runs, what needs a look. */
export default function ProjectPanel({ title, settings, home, now }: PanelProps<{ project?: string }>) {
  const name = typeof settings.project === "string" ? settings.project : "";
  const p = home.categories.flatMap((c) => c.projects).find((x) => x.name === name);
  const state: State = !p ? "idle" : p.needs > 0 || p.last === "failed" ? "bad" : p.last === "succeeded" ? "ok" : p.running > 0 ? "run" : "idle";
  return (
    <section className="pn" aria-label={title}>
      <h2>{title}</h2>
      <div className="pn-b">
        {!p ? <p className="faint">{name ? `${name} is not in this view, or you cannot see it.` : terms().say("No {project} chosen.")}</p> : <>
          <p><Status state={state} label={p.needs > 0 ? `${p.needs} need a look` : p.last ?? "No result yet"} /> <a href={`/projects/${encodeURIComponent(p.name)}/pipeline`} onClick={onLink}><b>{p.name}</b></a></p>
          <p className="small">{p.needs > 0 ? `${p.needs} ${p.needs === 1 ? "entry needs" : "entries need"} a look. ` : ""}{p.last ? `Latest on the default branch ${p.last}${p.last_at ? ` ${ago(p.last_at, now)}` : ""}.` : "No result on the default branch yet."} {p.running > 0 ? `${p.running} running.` : "Nothing running."}</p>
        </>}
      </div>
    </section>
  );
}
