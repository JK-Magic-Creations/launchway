import { useCallback } from "react";
import { ErrorBox } from "../../components/ui";
import { useLoad } from "../../guided/useLoad";
import { Button, EmptyState, LinkButton, PageHead, Sheet } from "../../kit/layout";
import { navigate, onLink } from "../../router";
import { squadronsApi } from "../../squadronsApi";
import { terms } from "../../terms";
import { str, useUrlState } from "../../urlState";
import { ADD_SQUADRON, addFlightHref, WhyNot } from "./shared";
import "../../squadrons.css";

/** Where every "Add a Flight" button leads: a Flight lives in a Squadron, so the person picks one (or makes one) and goes on. */
export function ChooseSquadronPage() {
  const fn = useCallback(() => squadronsApi.list(), []);
  const { data, error } = useLoad(fn);
  const rows = data?.squadrons ?? [];
  const [chosen, setChosen] = useUrlState("unit", rows.length === 1 ? rows[0].name : "", str);
  const t = terms();
  if (error && !data) return <div className="page"><ErrorBox error={error} /></div>;
  if (!data) return <div className="page"><div className="loading">Loading…</div></div>;
  const head = <PageHead eyebrow={t.say("{Projects}")} title={t.say("Add a {project}")}
    sentence={t.say("A {project} is one repository and its pipelines, and it lives in a {unit}. Choose the {unit} it belongs to.")} />;
  if (!data.may_manage) return <div className="page sq-page">{head}<Sheet><WhyNot why={data.why ?? ""} /></Sheet></div>;
  if (rows.length === 0) {
    return (
      <div className="page sq-page">{head}
        <Sheet><EmptyState title={t.say("Add a {unit} first")} action={<LinkButton href={ADD_SQUADRON} kind="pri" icon="plus">{t.say("Add a {unit}")}</LinkButton>}>
          {t.say("A {unit} is a named folder of {projects} with its own people and shared settings. You have none yet: make one, then add the {project} to it.")}
        </EmptyState></Sheet>
      </div>
    );
  }
  const on = chosen && rows.some((r) => r.name === chosen) ? chosen : "";
  return (
    <div className="page sq-page">{head}
      <Sheet pad>
        <form className="sq-form" onSubmit={(e) => { e.preventDefault(); if (on) navigate(addFlightHref(on)); }}>
          <fieldset className="sq-choices">
            <legend>{t.say("Which {unit}?")}</legend>
            {rows.map((s) => (
              <label key={s.name} className="sq-choice">
                <input type="radio" name="unit" value={s.name} checked={on === s.name} onChange={() => setChosen(s.name)} />
                <span><b>{s.display_name || s.name}</b>{s.display_name && s.display_name !== s.name ? <span className="faint mono"> {s.name}</span> : null}
                  <span className="faint small"> · {s.flights} {s.flights === 1 ? t.lower("project") : t.pluralLower("project")}{s.description ? ` · ${s.description}` : ""}</span></span>
              </label>
            ))}
          </fieldset>
          <div className="sq-actions">
            <Button kind="pri" type="submit" disabled={!on}>Continue</Button>
            <LinkButton href={ADD_SQUADRON} icon="plus">{t.say("Add a {unit}")}</LinkButton>
          </div>
        </form>
      </Sheet>
      <p className="faint small sq-aside">Need a connection to a Git service, or a custom place to read the pipeline from? <a href="/config/wizard/connect-repository" onClick={onLink}>Use the full connect wizard</a>.</p>
    </div>
  );
}
