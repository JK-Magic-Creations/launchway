import { useCallback } from "react";
import { ErrorBox } from "../../components/ui";
import { EntityLink } from "../../components/EntityLink";
import { useLoad } from "../../guided/useLoad";
import { Band, EmptyState, PageHead, Sheet } from "../../kit/layout";
import { Chip } from "../../kit/Status";
import { onLink } from "../../router";
import { squadronsApi, type SquadronList } from "../../squadronsApi";
import { terms } from "../../terms";
import { AddButtons, AddSquadronButton, squadronHref, WhyNot } from "./shared";
import "../../squadrons.css";

/** Every Squadron, with how many Flights it holds and what needs a person; below, the Flights that are in none. */
export function UnitListPage() {
  const fn = useCallback(() => squadronsApi.list(), []);
  const { data, error } = useLoad<SquadronList>(fn, 20000);
  const t = terms();
  if (error && !data) return <div className="page"><ErrorBox error={error} /></div>;
  if (!data) return <div className="page"><div className="loading">Loading…</div></div>;
  const rows = data.squadrons ?? [];
  const unfiled = data.unfiled ?? [];
  const needs = rows.reduce((n, s) => n + s.needs, 0);
  return (
    <div className="page sq-page">
      <PageHead eyebrow={t.say("{Units}")} title={t.say("{Units}")}
        sentence={rows.length === 0 ? t.say("A {unit} is a named folder of {projects} with its own people and shared settings. None exists yet.")
          : needs > 0 ? <><b>{needs} {needs === 1 ? "thing needs" : "things need"} a person</b> across the {t.pluralLower("unit")}.</> : t.say("Nothing in any {unit} needs a person.")}
        actions={<AddButtons />} />
      {rows.length > 0 && (
        <Band cells={[
          { key: "n", label: t.say("{Units}"), value: rows.length, foot: t.say("named folders of {projects}") },
          { key: "f", label: t.say("{Projects}"), value: rows.reduce((n, s) => n + s.flights, 0), foot: unfiled.length ? `${unfiled.length} not in a folder yet` : "all filed" },
          { key: "bad", label: "Failing", value: rows.reduce((n, s) => n + s.failing, 0), loud: rows.some((s) => s.failing) ? "bad" : undefined, foot: "latest result failed" },
        ]} />
      )}
      {rows.length === 0 ? (
        <Sheet><EmptyState title={t.say("No {units} yet")} action={data.may_manage ? <AddSquadronButton primary /> : <WhyNot why={data.why ?? ""} />}>
          {t.say("A {unit} is a named folder of {projects} with its own people and shared settings. Make one, then add the {projects} that belong in it.")}
        </EmptyState></Sheet>
      ) : (
        <Sheet title={t.say("Every {unit}")}>
          <ul className="rowlist">
            {rows.map((s) => (
              <li key={s.name} className={s.failing > 0 ? "bad" : ""}>
                <div className="main">
                  <div className="nm"><a href={squadronHref(s.name)} onClick={onLink}>{s.display_name || s.name}</a>{s.display_name && s.display_name !== s.name && <span className="faint mono"> {s.name}</span>}</div>
                  <div className="sb">{s.description || ""}</div>
                </div>
                <div className="cell">{s.flights} {s.flights === 1 ? t.lower("project") : t.pluralLower("project")}</div>
                <div className="cell sq-chips">
                  {s.needs > 0 && <Chip tone="warn">{s.needs} need a look</Chip>}
                  {s.failing > 0 && <Chip tone="bad">{s.failing} failing</Chip>}
                  {s.running > 0 && <Chip tone="run">{s.running} running</Chip>}
                  {s.needs === 0 && s.failing === 0 && s.running === 0 && <Chip tone="ok">all quiet</Chip>}
                </div>
              </li>
            ))}
          </ul>
        </Sheet>
      )}
      {unfiled.length > 0 && (
        <Sheet title={`Unfiled (${unfiled.length})`}>
          <p className="sh-b faint">{t.say("These {projects} are in no {unit} yet.")}</p>
          <ul className="rowlist">
            {unfiled.map((f) => <li key={f.name}><div className="main"><div className="nm"><EntityLink kind="pipeline" name={f.name} /></div></div></li>)}
          </ul>
        </Sheet>
      )}
    </div>
  );
}
