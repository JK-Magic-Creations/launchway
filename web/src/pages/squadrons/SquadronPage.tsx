import { useCallback } from "react";
import { ApiError } from "../../api";
import { ErrorBox, Empty } from "../../components/ui";
import { EntityLink } from "../../components/EntityLink";
import { ago } from "../../format";
import { useLoad } from "../../guided/useLoad";
import { Icon } from "../../kit/Icon";
import { Band, EmptyState, LinkButton, PageHead, Sheet } from "../../kit/layout";
import { Chip, Status, stateOf } from "../../kit/Status";
import { Tabs } from "../../kit/Tabs";
import { useNow } from "../../live";
import { onLink } from "../../router";
import { squadronsApi, type SquadronPage } from "../../squadronsApi";
import { terms } from "../../terms";
import { oneOf, useUrlState, useViewTitle } from "../../urlState";
import { FlightCheck, MoveFlightsBar, SelectAllFlights, useFlightSelection } from "../../components/MoveFlights";
import { ProjectCard } from "../HomeSections";
import { addFlightHref, SQUADRONS, WhyNot } from "./shared";
import "../../squadrons.css";
import "../../config.css";

const TABS = ["flights", "people", "operations"] as const;
type Tab = (typeof TABS)[number];
const tabCodec = oneOf<Tab>(...TABS);

/** One Squadron: its Flights with their latest result, what needs a person, who holds a role and which shared settings
 * it sets, and the pipelines only a person starts. The tab is in the address, so a view can be bookmarked. */
export function SquadronViewPage({ name }: { name: string }) {
  const fn = useCallback(() => squadronsApi.show(name), [name]);
  const { data, error, reload } = useLoad<SquadronPage>(fn, 15000);
  const selection = useFlightSelection((data?.flights ?? []).map((f) => f.name));
  const [tab, setTab] = useUrlState("tab", "flights" as Tab, tabCodec);
  const now = useNow(1000);
  const t = terms();
  useViewTitle(tab === "people" ? "People and shared settings" : tab === "operations" ? "Operations" : "");

  if (error && !data) {
    if (error instanceof ApiError && error.status === 404) {
      return <div className="page sq-page"><Empty title={t.say("There is no {unit} called " + name)}>{t.say("It may have been renamed or removed.")} <a href={SQUADRONS} onClick={onLink}>{t.say("See every {unit}")}</a>.</Empty></div>;
    }
    return <div className="page sq-page"><ErrorBox error={error} /></div>;
  }
  if (!data) return <div className="page"><div className="loading">Loading…</div></div>;

  const flights = data.flights ?? [];
  const needs = data.needs ?? [];
  const failing = flights.filter((f) => f.last === "failed").length;
  const running = flights.reduce((n, f) => n + f.running, 0);
  const title = data.display_name || data.name;
  const addHref = addFlightHref(data.name);
  const addFlight = data.may_manage ? <LinkButton href={addHref} kind="pri" icon="plus">{t.say("Add a {project}")}</LinkButton> : <WhyNot why={data.why ?? ""} />;

  return (
    <div className="page sq-page">
      <PageHead eyebrow={t.say("{Unit}")} title={title}
        sentence={data.description || (flights.length === 0 ? t.say("No {projects} yet.") : needs.length > 0 ? <><b>{needs.length} {needs.length === 1 ? "thing needs" : "things need"} a person.</b></> : failing > 0 ? `${failing} of ${flights.length} latest results failed.` : "Everything here is fine or still going.")}
        actions={flights.length > 0 ? addFlight : undefined} />
      {flights.length > 0 && (
        <Band cells={[
          { key: "n", label: t.say("{Projects}"), value: flights.length, foot: "in this folder" },
          { key: "needs", label: "Need a look", value: needs.length, loud: needs.length ? "warn" : undefined, foot: needs.length ? "listed below" : "nothing" },
          { key: "bad", label: "Failing", value: failing, loud: failing ? "bad" : undefined, foot: failing ? "latest result failed" : "none" },
          { key: "run", label: "Running now", value: running, foot: running ? "work is moving" : "quiet" },
        ]} />
      )}
      {needs.length > 0 && (
        <Sheet title={<>Needs you <span className="vchip warn">{needs.length}</span></>}>
          {needs.map((n) => (
            <div key={n.key} className={`need ${n.tone}`}>
              <div className={`ico-tile ${n.tone}`}><Icon name={n.kind === "failed" ? "x" : n.kind === "decision" ? "gate" : n.kind === "deployment" ? "alert" : "server"} /></div>
              <div className="tx">
                <div className="ti"><a href={n.href} onClick={onLink}>{n.title}</a> <span className="faint small">{ago(n.since, now)}</span></div>
                <div className="ds"><b>Why it is here:</b> {n.why}</div>
                <div className="ds"><b>What to do:</b> {n.do}</div>
              </div>
              <div className="acts"><LinkButton href={n.href} sm kind="pri">Open</LinkButton></div>
            </div>
          ))}
        </Sheet>
      )}
      <Tabs label={`${title}: views`} on={tab} onChange={(id) => setTab(id as Tab, "push")}
        tabs={[{ id: "flights", label: t.say("{Projects}") }, { id: "people", label: "People and shared settings" }, { id: "operations", label: "Operations" }]} />
      <div role="tabpanel" aria-labelledby={`tab-${tab}`} className="sq-panel">
        {tab === "flights" && (flights.length === 0 ? (
          <Sheet><EmptyState title={t.say("This {unit} has no {projects} yet")} action={addFlight}>
            {t.say("A {unit} is a named folder of {projects} with its own people and shared settings. A {project} is one repository and its pipelines. Add the first one and its results appear here.")}
          </EmptyState></Sheet>
        ) : (
          <Sheet title={t.say("{Projects}")} actions={data.may_manage ? <SelectAllFlights selection={selection} /> : undefined}>
            {data.may_manage && <MoveFlightsBar selection={selection} onMoved={reload} />}
            {data.may_manage && <div className="sq-checks">{flights.map((f) => <label key={f.name} className="sq-check"><FlightCheck selection={selection} name={f.name} /> <span>{f.name}</span></label>)}</div>}
            <ul className="hm-cards">
              {[...flights].sort((a, b) => b.needs - a.needs || Number(b.last === "failed") - Number(a.last === "failed") || a.name.localeCompare(b.name)).map((f) => <ProjectCard key={f.name} p={f} now={now} />)}
            </ul>
          </Sheet>
        ))}
        {tab === "people" && <PeopleTab data={data} />}
        {tab === "operations" && <OperationsTab data={data} now={now} />}
      </div>
    </div>
  );
}

function PeopleTab({ data }: { data: SquadronPage }) {
  const t = terms();
  const people = data.people ?? [];
  const settings = data.settings ?? [];
  return (
    <>
      <Sheet title="People with a role here" actions={<LinkButton href="/settings/access" sm>People and access</LinkButton>}>
        {!data.may_manage ? <div className="sh-b"><WhyNot why={data.why ?? ""} /></div>
          : people.length === 0 ? <p className="sh-b faint">{t.say("No one has been given a role on this {unit} itself. People keep the roles they hold above it.")}</p>
          : <ul className="rowlist">{people.map((p) => <li key={p.who + p.role}><div className="main"><div className="nm">{p.who}</div></div><div className="cell"><Chip>{p.role}</Chip></div></li>)}</ul>}
      </Sheet>
      <Sheet title={t.say("Settings shared by its {projects}")} actions={<LinkButton href={`/config/units/${encodeURIComponent(data.name)}`} sm>{t.say("Edit this {unit}")}</LinkButton>}>
        {!data.may_manage ? <div className="sh-b"><WhyNot why={data.why ?? ""} /></div>
          : settings.length === 0 ? <p className="sh-b faint">{t.say("This {unit} sets nothing of its own: its {projects} use the settings of the levels above.")}</p>
          : <ul className="rowlist">{settings.map((s) => (
            <li key={s.kind}>
              <div className="main"><div className="nm"><a href={`/config/${encodeURIComponent(s.path)}`} onClick={onLink}>{s.title}</a></div></div>
              <div className="cell">{s.count} set here</div>
            </li>))}</ul>}
      </Sheet>
    </>
  );
}

function OperationsTab({ data, now }: { data: SquadronPage; now: number }) {
  const ops = data.operations ?? [];
  const t = terms();
  return (
    <Sheet title="Started by hand">
      <p className="sh-b faint">{t.say("These pipelines have no trigger: nothing starts them but a person. They are how operations work (a rollout, a restore, a cleanup) is done here.")}</p>
      {data.operations_complete === false && <p className="sh-b sq-note" role="status">This list may be missing some. Reading every pipeline took too long, so not every {t.lower("project")} was checked. Open a {t.lower("project")} to see its own.</p>}
      {ops.length === 0 ? <p className="sh-b faint">{data.operations_complete === false ? "None found in the time allowed." : "None: something starts every pipeline here."}</p> : (
        <ul className="rowlist">
          {ops.map((o) => (
            <li key={o.flight + "/" + o.pipeline}>
              <div className="main">
                <div className="nm"><EntityLink kind="pipeline" name={o.pipeline} id={o.flight} /> <span className="faint small">in {o.flight}</span></div>
              </div>
              <div className="cell">
                {o.last ? <><Status state={stateOf(o.last as never)} /><span>{o.last_id ? <EntityLink kind="run" name={`#${o.last_number}`} id={o.last_id} /> : null} {o.last_at ? ago(o.last_at, now) : ""}</span></> : <span className="faint">Never started</span>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Sheet>
  );
}
