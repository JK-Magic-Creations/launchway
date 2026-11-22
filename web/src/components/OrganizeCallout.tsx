import { useEffect, useState } from "react";
import { onLink } from "../router";
import { squadronsApi } from "../squadronsApi";
import { terms } from "../terms";
import "../moveflights.css";

/** "N Flights are in no Squadron. Organize them": shown only when there are unfiled Flights and the person may manage
 * Squadrons. It says nothing (and asks nothing) otherwise. */
export function OrganizeCallout() {
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    squadronsApi.list().then((l) => { if (live) setN(l.may_manage ? (l.unfiled?.length ?? 0) : 0); }).catch(() => undefined);
    return () => { live = false; };
  }, []);
  if (n === 0) return null;
  const t = terms();
  return (
    <p className="mv-callout" role="note">
      {t.say(`${n} ${n === 1 ? "{project} is" : "{projects} are"} in no {unit}.`)} <a href="/units/organize" onClick={onLink}>Organize {n === 1 ? "it" : "them"}</a>
    </p>
  );
}
