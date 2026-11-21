// The words editor: choose the aviation set or the plain business set, see the navigation and a sample page with
// the choice, rename any single word, and reset. It is shown on the first-run wizard's Words step and on
// Settings → Words, and changes nothing by itself: the page that holds it saves.

import { Choices } from "./ConfigForm";
import { Button, Decision } from "../kit/layout";
import { Icon, type IconName } from "../kit/Icon";
import { SETS, TERM_META, isSetId, makeTerms, termProblem, type SetId, type Terms } from "../terms";

export interface WordsValue { set: SetId; overrides: Record<string, string> }

/** The four level words have a plural that is asked for too. */
const LEVELS = ["installation", "organization", "unit", "project"];

export function WordsEditor({ value, onChange }: { value: WordsValue; onChange: (v: WordsValue) => void }) {
  const t = makeTerms(value);
  const base = makeTerms({ set: value.set });
  const setOne = (key: string, text: string) => {
    const next = { ...value.overrides };
    if (text === "") delete next[key]; else next[key] = text;
    onChange({ ...value, overrides: next });
  };
  const renamed = Object.keys(value.overrides).length;
  return (
    <div className="words">
      <Choices legend="Which words should Launchway use?" value={value.set} onChange={(v) => isSetId(v) && onChange({ ...value, set: v })}
        help="Only the words on the screen change: addresses, commands, pipeline files and settings keep their plain names, so nothing you script depends on a word."
        options={(Object.keys(SETS) as SetId[]).map((id) => ({ value: id, label: SETS[id].name + (id === "aviation" ? " (default)" : ""), help: SETS[id].blurb }))} />

      <WordsPreview t={t} />

      <fieldset className="cf-section words-list">
        <legend className="cf-section-title">Every word, one by one</legend>
        <p className="cf-help">Leave a box empty to use the set's word. Words everyone knows (project, pipeline, step, run, environment, template, variable, branch, commit) are not listed: they are never renamed.</p>
        <table className="table words-table">
          <thead><tr><th>What it is</th><th>In this set</th><th>Your word</th>{<th className="words-pl">Several</th>}<th /></tr></thead>
          <tbody>
            {TERM_META.map((m) => {
              const own = value.overrides[m.key] ?? "";
              const ownPl = value.overrides[m.key + "_plural"] ?? "";
              const bad = own ? termProblem(own) : null;
              const badPl = ownPl ? termProblem(ownPl) : null;
              const level = LEVELS.includes(m.key);
              return (
                <tr key={m.key} data-term={m.key}>
                  <td>{m.means}</td>
                  <td><b>{base.label(m.key)}</b></td>
                  <td>
                    <input className="cf-input" aria-label={`Your word for ${m.key.replace(/_/g, " ")}`} value={own} placeholder={base.label(m.key)} maxLength={60}
                      aria-invalid={bad ? true : undefined} onChange={(e) => setOne(m.key, e.target.value)} />
                    {bad && <div className="cf-problem" role="alert">{bad}</div>}
                  </td>
                  <td className="words-pl">
                    {level ? <>
                      <input className="cf-input" aria-label={`Your plural for ${m.key}`} value={ownPl} placeholder={base.plural(m.key)} maxLength={60}
                        aria-invalid={badPl ? true : undefined} onChange={(e) => setOne(m.key + "_plural", e.target.value)} />
                      {badPl && <div className="cf-problem" role="alert">{badPl}</div>}
                    </> : null}
                  </td>
                  <td>{(own || ownPl) && <Button sm onClick={() => { const n = { ...value.overrides }; delete n[m.key]; delete n[m.key + "_plural"]; onChange({ ...value, overrides: n }); }}>Reset</Button>}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        <div className="cf-row">
          <Button disabled={renamed === 0 && value.set === "aviation"} onClick={() => onChange({ set: "aviation", overrides: {} })}>Reset to the defaults</Button>
          <span className="faint small">{renamed === 0 ? "No word is renamed." : `${renamed} ${renamed === 1 ? "word is" : "words are"} renamed.`}</span>
        </div>
      </fieldset>
    </div>
  );
}

/** Whether every override is acceptable to the server. */
export function wordsProblems(v: WordsValue): string | null {
  for (const [k, x] of Object.entries(v.overrides)) {
    const p = termProblem(x);
    if (p) return `${k.replace(/_/g, " ")}: ${p}`;
  }
  return null;
}

const NAV: { icon: IconName; label: (t: Terms) => string }[] = [
  { icon: "home", label: () => "Home" },
  { icon: "code", label: () => "Code" },
  { icon: "graph", label: () => "Pipelines" },
  { icon: "deploy", label: () => "Deployments" },
  { icon: "activity", label: () => "Activity" },
  { icon: "calendar", label: (t) => `Changes: ${t.lower("hold")} windows` },
  { icon: "gate", label: (t) => t.plural("clearance") },
  { icon: "clock", label: (t) => t.label("logbook") },
];

/** The words in use: the navigation, a trail through the levels, and a sample decision, drawn with the chosen words. */
export function WordsPreview({ t }: { t: Terms }) {
  return (
    <section className="words-preview" aria-label="Preview with these words" data-set={t.set}>
      <div className="eyebrow">Preview</div>
      <div className="wp-grid">
        <nav className="wp-nav" aria-label="Navigation preview">
          {NAV.map((n, i) => <span key={i} className={"wp-item" + (i === 1 ? " on" : "")}><Icon name={n.icon} size="s" />{n.label(t)}</span>)}
        </nav>
        <div className="wp-page">
          <ol className="wp-crumbs" aria-label="Trail preview">
            {["installation", "organization", "unit", "project"].map((k) => <li key={k}><span className="faint">{t.label(k)}</span></li>)}
          </ol>
          <Decision tone="warn" title={`Release 2.4 needs ${t.lower("clearance")}`}
            children={`The ${t.lower("payload")} is one change to the checkout service. The ${t.lower("preflight")} passed; ${t.lower("hold")} windows do not apply today.`}
            actions={<div className="decide-form">
              <p className="small faint" style={{ margin: 0 }}>{t.label("launch_line")}: {t.lower("in_flight")}, waiting for a person.</p>
              <div className="acts decide">
                <Button kind="ok" icon="check" tabIndex={-1}>{t.label("go")}</Button>
                <Button kind="bad" icon="x" tabIndex={-1}>{t.label("no_go")}</Button>
              </div>
            </div>} />
        </div>
        <aside className="wp-guide"><span className="tag">{t.label("guide")}</span><p>Say what you want to do, and the {t.lower("guide")} shows the way. Your {t.lower("flight_plan")} is the pipeline a new {t.lower("project")} starts from.</p></aside>
      </div>
    </section>
  );
}
