import { useCallback, type ReactNode } from "react";
import { useMe } from "../../auth";
import { useLoad } from "../../guided/useLoad";
import { LinkButton } from "../../kit/layout";
import { squadronsApi, type SquadronList } from "../../squadronsApi";
import { terms } from "../../terms";
import "../../squadrons.css";

// What the pages about Squadrons and Flights share: the name rule, the addresses, and who may make them.
// The permanent words in the addresses are `units` and `projects`; every word a person reads comes from the terms.

/** The same rule and the same plain messages as the server (internal/settings/squadrons.go, UnitNameProblem). */
export function unitNameProblem(name: string): string {
  if (name.trim() === "") return terms().say("Give the {unit} a name.");
  if (name !== name.trim()) return "A name does not start or end with a space.";
  if (name.length > 100) return "A name is at most 100 characters.";
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name)) return "A name uses letters, digits, '.', '_' and '-', and starts with a letter or a digit. Write the longer name you want people to read as the display name.";
  return "";
}

export const ADD_SQUADRON = "/units/new";
export const ADD_FLIGHT = "/projects/new";
export const SQUADRONS = "/units";
export const squadronHref = (name: string) => `/units/${encodeURIComponent(name)}`;
export const addFlightHref = (unit: string) => `/units/${encodeURIComponent(unit)}/projects/new`;

const listFn = () => squadronsApi.list();

/** Whether this person may make Squadrons and Flights, and if not, why. Until the server has answered, an administrator
 * is assumed to be allowed and anybody else is shown nothing (so a button never flashes in and out). */
export function useSquadronAccess(): { may: boolean | null; why: string; list: SquadronList | null; reload: () => void } {
  const me = useMe();
  const { data, reload } = useLoad(useCallback(listFn, []));
  const known = data && typeof data.may_manage === "boolean" ? data : null;
  return { may: known ? known.may_manage : me?.admin ? true : null, why: known?.why ?? "", list: known, reload };
}

/** The reason a person may not make a Squadron or a Flight, in words, instead of a missing button. */
export function WhyNot({ why }: { why: string }) {
  return <p className="sq-why" role="note">{why || terms().say("Making {units} and {projects} needs the administrator role.")}</p>;
}

function Gate({ children, small }: { children: ReactNode; small?: boolean }) {
  const { may, why } = useSquadronAccess();
  if (may === null) return null;
  if (!may) return <span className={"sq-why" + (small ? " small" : "")}>{why || terms().say("Making {units} and {projects} needs the administrator role.")}</span>;
  return <>{children}</>;
}

/** "Add a Squadron": a button to the page that asks for a name; for people who may not, the reason. */
export function AddSquadronButton({ sm, primary }: { sm?: boolean; primary?: boolean }) {
  return <Gate small={sm}><LinkButton href={ADD_SQUADRON} sm={sm} kind={primary ? "pri" : undefined} icon="plus">{terms().say("Add a {unit}")}</LinkButton></Gate>;
}

/** "Add a Flight": a button to the chooser page, or to the Squadron's own page when one is named. */
export function AddFlightButton({ sm, primary, unit }: { sm?: boolean; primary?: boolean; unit?: string }) {
  return <Gate small={sm}><LinkButton href={unit ? addFlightHref(unit) : ADD_FLIGHT} sm={sm} kind={primary ? "pri" : undefined} icon="plus">{terms().say("Add a {project}")}</LinkButton></Gate>;
}

/** Both buttons together, or the one reason: for the top of a list of pipelines or repositories. */
export function AddButtons({ sm, flightFirst }: { sm?: boolean; flightFirst?: boolean }) {
  const { may, why } = useSquadronAccess();
  if (may === null) return null;
  if (!may) return <span className={"sq-why" + (sm ? " small" : "")}>{why || terms().say("Making {units} and {projects} needs the administrator role.")}</span>;
  const unit = <LinkButton key="u" href={ADD_SQUADRON} sm={sm} icon="plus">{terms().say("Add a {unit}")}</LinkButton>;
  const flight = <LinkButton key="f" href={ADD_FLIGHT} sm={sm} kind="pri" icon="plus">{terms().say("Add a {project}")}</LinkButton>;
  return <>{flightFirst ? [flight, unit] : [unit, flight]}</>;
}

/** The name a Flight gets from its repository, the same way the server makes it (flightNameFrom). */
export function flightNameFrom(repo: string): string {
  let s = repo.trim().replace(/\/+$/, "").replace(/\.git$/, "");
  const i = Math.max(s.lastIndexOf("/"), s.lastIndexOf(":"));
  if (i >= 0) s = s.slice(i + 1);
  return s.replace(/[^A-Za-z0-9._-]/g, "-").replace(/^[-.]+|[-.]+$/g, "");
}

/** What is wrong with a Flight's name, in a sentence, or "". */
export function flightNameProblem(name: string): string {
  if (name.trim() === "") return terms().say("Give the {project} a name.");
  if (name.length > 100 || !/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name)) return terms().say("A {project}'s name uses letters, digits, '.', '_' and '-', and starts with a letter or a digit.");
  return "";
}
