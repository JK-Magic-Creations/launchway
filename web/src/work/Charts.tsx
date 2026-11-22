import { Sheet } from "../kit/layout";
import { Blank, ErrorPanel, Loading, scopeLabel, useLoad, WorkPage } from "./common";
import { Bars, LAYER_CLASSES, Legend, PairBars, StackedAreas } from "./chartParts";
import { workApi, type Cfd, type TimeStats, type Velocity } from "./workApi";
import { TdNum, ThCol, ThRow } from "../kit/bits";

// Velocity, cumulative flow, and how long work takes. Each chart has the same numbers in a table beside it.

export default function Charts({ scope }: { scope: string }) {
  const v = useLoad<Velocity>("velocity|" + scope, () => workApi.velocity(scope));
  const f = useLoad<Cfd>("flow|" + scope, () => workApi.flow(scope));
  const t = useLoad<TimeStats>("times|" + scope, () => workApi.times(scope));
  return (
    <WorkPage scope={scope} on="charts" title="Charts" sentence={`How ${scopeLabel(scope)} is doing: what each sprint delivers, where work waits, and how long it takes.`}>
      <Sheet title="Velocity" pad>
        <ErrorPanel error={v.error} onRetry={v.reload} />
        {v.loading && <Loading />}
        {v.data && v.data.sprints.length === 0 && <Blank title="No finished sprints yet">Velocity is what each completed sprint delivered. Complete a sprint to see it.</Blank>}
        {v.data && v.data.sprints.length > 0 && (
          <>
            <PairBars name="Velocity by sprint" unit={v.data.unit} rows={v.data.sprints.map((s) => ({ label: s.sprint, a: s.committed, b: s.completed }))} />
            <Legend items={[{ label: "Committed", cls: "s-a" }, { label: "Completed", cls: "s-b" }]} />
            <p className="small">Average completed: <strong className="num">{v.data.average.toFixed(1)} {v.data.unit}</strong> a sprint.</p>
            <div className="tablewrap"><table className="table wk-table">
              <caption>Velocity by sprint, in {v.data.unit}</caption>
              <thead><tr><ThCol>Sprint</ThCol><ThCol>Committed</ThCol><ThCol>Completed</ThCol></tr></thead>
              <tbody>{v.data.sprints.map((s) => <tr key={s.sprint}><ThRow>{s.sprint}</ThRow><TdNum>{s.committed}</TdNum><TdNum>{s.completed}</TdNum></tr>)}</tbody>
            </table></div>
          </>
        )}
      </Sheet>
      <Sheet title="Cumulative flow" pad>
        <ErrorPanel error={f.error} onRetry={f.reload} />
        {f.loading && <Loading />}
        {f.data && f.data.days.length === 0 && <Blank title="No flow to show yet">Once items move between states, the number in each state is drawn day by day.</Blank>}
        {f.data && f.data.days.length > 0 && <Flow data={f.data} />}
      </Sheet>
      <Sheet title="Cycle and lead time" pad>
        <ErrorPanel error={t.error} onRetry={t.reload} />
        {t.loading && <Loading />}
        {t.data && t.data.count === 0 && <Blank title="Nothing has finished yet">Lead time runs from creation to done; cycle time from the first work to done. Finished items appear here.</Blank>}
        {t.data && t.data.count > 0 && (
          <>
            <Bars name="Items by how many days they took" unit="items" rows={t.data.histogram.map((b) => ({ label: b.label, value: b.count }))} />
            <div className="tablewrap"><table className="table wk-table">
              <caption>Items by days taken, {t.data.count} finished</caption>
              <thead><tr><ThCol>Days</ThCol><ThCol>Items</ThCol></tr></thead>
              <tbody>{t.data.histogram.map((b) => <tr key={b.label}><ThRow>{b.label}</ThRow><TdNum>{b.count}</TdNum></tr>)}</tbody>
            </table></div>
            <div className="tablewrap"><table className="table wk-table">
              <caption>Typical times, in days</caption>
              <thead><tr><ThCol>Measure</ThCol><ThCol>Median</ThCol><ThCol>85th percentile</ThCol><ThCol>Mean</ThCol></tr></thead>
              <tbody>
                <tr><ThRow>Lead time</ThRow><TdNum>{t.data.lead_median_days.toFixed(1)}</TdNum><TdNum>{t.data.lead_p85_days.toFixed(1)}</TdNum><TdNum>{t.data.lead_mean_days.toFixed(1)}</TdNum></tr>
                <tr><ThRow>Cycle time</ThRow><TdNum>{t.data.cycle_median_days.toFixed(1)}</TdNum><TdNum>{t.data.cycle_p85_days.toFixed(1)}</TdNum><TdNum>{t.data.cycle_mean_days.toFixed(1)}</TdNum></tr>
              </tbody>
            </table></div>
          </>
        )}
      </Sheet>
    </WorkPage>
  );
}

function Flow({ data }: { data: Cfd }) {
  const days = data.days.map((d) => d.date);
  // Stack the last state at the bottom, so work that is done sits under the work still to do.
  const layers = data.states.slice().reverse().map((s, i) => ({ label: s.name, key: s.key, cls: LAYER_CLASSES[i % LAYER_CLASSES.length]!, values: data.days.map((d) => d.counts[s.key] ?? 0) }));
  const shown = data.days.slice(-14);
  return (
    <>
      <StackedAreas name="Cumulative flow: items in each state by day" days={days} layers={layers} />
      <Legend items={layers.slice().reverse().map((l) => ({ label: l.label, cls: l.cls }))} />
      <div className="tablewrap"><table className="table wk-table">
        <caption>Items in each state, last {shown.length} days</caption>
        <thead><tr><ThCol>Date</ThCol>{data.states.map((s) => <ThCol key={s.key}>{s.name}</ThCol>)}</tr></thead>
        <tbody>{shown.map((d) => <tr key={d.date}><ThRow>{d.date}</ThRow>{data.states.map((s) => <TdNum key={s.key}>{d.counts[s.key] ?? 0}</TdNum>)}</tr>)}</tbody>
      </table></div>
    </>
  );
}
