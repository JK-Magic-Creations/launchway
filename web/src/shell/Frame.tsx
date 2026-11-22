import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, type Approval, type Me } from "../api";
import { attention } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { announce } from "../kit/announce";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { Icon } from "../kit/Icon";
import { CrumbLabelContext } from "../nav/crumbs";
import { Breadcrumbs } from "../nav/Navigation";
import { isLearn, pageTitle, redirectFor, section, sectionOf, visibleTabs, type Ctx, type SectionId } from "../nav/model";
import { PageFor } from "../nav/pages";
import type { Approvals } from "../live";
import { navigate, onLink, replace, usePath } from "../router";
import { DoBarButton } from "./DoBarButton";
import { ExplainerContext, type Explainer } from "./explainer";
import { Launcher } from "./Launcher";
import { Pulse, Route, TabBar, type PulseState } from "./Route";
import { SideNav } from "./SideNav";
import { shortcutOn } from "./prefs";
import { useFocusableScrollers } from "./scrollers";
import { currentTheme, setTheme } from "./theme";
import { UserMenu } from "./UserMenu";
import { terms, useTerms } from "../terms";
import { currentViewTitle, onViewTitle } from "../urlState";

// The finder and the Do bar load when first opened.
const Shortcuts = lazy(() => import("./Shortcuts"));
const DoBar = lazy(() => import("./DoBar").then((m) => ({ default: m.DoBar })));
const Guide = lazy(() => import("./Guide").then((m) => ({ default: m.Guide }))); // the first load does not carry the Guide's text
const GUIDE_KEY = "lw-guide";

function storedGuide(): boolean | null {
  try { const v = localStorage.getItem(GUIDE_KEY); return v === "1" ? true : v === "0" ? false : null; } catch { return null; }
}

/** The pulse: one line for the whole installation. */
export function pulseOf(needsBad: number, needsWarn: number, running: boolean): PulseState {
  return needsBad > 0 ? "failing" : needsWarn > 0 ? "waiting" : running ? "running" : "quiet";
}

/** Whether a key press should open the do bar: the slash key, outside a field, with no modifier. */
export function opensDoBar(e: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "altKey" | "target">): boolean {
  if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || !shortcutOn()) return false;
  const t = e.target as HTMLElement | null;
  const tag = t?.tagName;
  return !(tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || t?.isContentEditable);
}

/** Whether the screen is wide enough to dock the Guide beside the page. */
function useWide(): boolean {
  const q = "(min-width: 1101px)";
  const [wide, setWide] = useState(() => typeof matchMedia !== "function" || matchMedia(q).matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const m = matchMedia(q);
    const on = () => setWide(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return wide;
}

/** Frame is the whole page around a screen: the top route with the pulse, the do bar, the left column
 * of the area, breadcrumbs, the page, the Guide, and on a phone the bottom bar. */
export function Frame({ me, approvals }: { me: Me; approvals: Approvals }) {
  const path = usePath();
  useTerms(); // the page draws again when the words change
  const inst = useInstallation();
  const [version, setVersion] = useState("");
  const [crumb, setCrumb] = useState<string | undefined>();
  const [layer, setLayer] = useState<"do" | "areas" | "keys" | null>(null);
  const [guidePref, setGuidePref] = useState<boolean | null>(storedGuide);
  const [explainer, setExplainer] = useState<Explainer | null>(null);
  const [theme, setThemeState] = useState(currentTheme);
  useEffect(() => { api.info().then((i) => setVersion(i.version)).catch(() => {}); }, []);
  // Signed in already: the sign-in page has nothing to offer.
  useEffect(() => { if (path === "/signin") navigate("/"); }, [path]);
  // An address from an older menu goes to where the page lives now.
  useEffect(() => {
    const to = redirectFor(path, location.search);
    if (to) replace(to);
  }, [path]);
  useEffect(() => {
    const set = () => { const v = currentViewTitle(); document.title = (v ? v + " · " : "") + pageTitle(path); };
    set();
    void import("../trail").then((m) => m.noteView(path)); // what was opened is kept in the person's own trail, in the background
    return onViewTitle(set);
  }, [path]);
  // A page change in a single-page app is silent: say where we are, and put the reading position at the top of the page.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) { first.current = false; return; }
    announce(pageTitle(path).replace(/ · Launchway$/, ""));
    document.getElementById("main")?.focus({ preventScroll: true });
  }, [path]);
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (e.defaultPrevented) return; if (opensDoBar(e)) { e.preventDefault(); setLayer("do"); } else if (e.key === "?" && opensDoBar({ key: "/", ctrlKey: e.ctrlKey, metaKey: e.metaKey, altKey: e.altKey, target: e.target })) { e.preventDefault(); setLayer("keys"); } };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, []);

  useFocusableScrollers();
  const ctx = { me, facts: inst.facts };
  const area = sectionOf(path);
  const needs = useMemo(() => attention({ projects: inst.projects, approvals: approvals.list ?? ([] as Approval[]), runners: inst.runners, applications: inst.applications, me }),
    [inst.projects, approvals.list, inst.runners, inst.applications, me]);
  // Failed runs and decisions are counted by the server's attention rule (the same answer as the Home page and the Sorties list); the
  // rest (an unhealthy application, a runner that does not answer) are counted here. Until the server answers, this page's own reading is used.
  const [server, setServer] = useState<{ bad: number; warn: number } | null>(null);
  useEffect(() => {
    let live = true;
    const tick = () => void import("../organizeApi").then((m) => m.organizeApi.attention(true)).then((a) => { if (live) setServer({ bad: a.needs.filter((n) => n.tone === "bad").length, warn: a.needs.filter((n) => n.tone === "warn").length }); }).catch(() => {});
    tick();
    const t = window.setInterval(tick, 30000);
    return () => { live = false; window.clearInterval(t); };
  }, [approvals.list]);
  const own = server ? needs.filter((n) => !/^(run|approval):/.test(n.key)) : needs;
  const badN = own.filter((n) => n.tone === "bad").length + (server?.bad ?? 0);
  const warnN = own.filter((n) => n.tone === "warn").length + (server?.warn ?? 0);
  const pulse = pulseOf(badN, warnN, inst.runs.some((r) => r.status === "running"));
  // Wide screens remember the choice, and open the Guide on Home. A narrow screen shows it as a sheet, closed until asked.
  const wide = useWide();
  // What needs a person is said once when it changes, not on every refresh.
  const lastPulse = useRef(pulse);
  useEffect(() => {
    if (lastPulse.current === pulse) return;
    lastPulse.current = pulse;
    if (pulse === "failing") announce("Something failed and needs you.", "assertive");
    else if (pulse === "waiting") announce("Something is waiting for a decision.");
    else if (pulse === "quiet") announce("Nothing needs you now.");
  }, [pulse]);
  const [sheetAt, setSheetAt] = useState<string | null>(null); // the page the sheet was opened on: it closes on the next page
  const sheet = sheetAt === path;
  const guideOpen = wide ? (guidePref ?? path === "/") : sheet;
  const toggleGuide = useCallback(() => {
    const next = !guideOpen;
    if (!wide) { setSheetAt(next ? path : null); if (!next) requestAnimationFrame(() => document.querySelector<HTMLElement>("[data-act='guide']")?.focus()); return; }
    setGuidePref(next);
    if (!next) requestAnimationFrame(() => document.querySelector<HTMLElement>("[data-act='guide']")?.focus());
    try { localStorage.setItem(GUIDE_KEY, next ? "1" : "0"); } catch { /* storage may be blocked */ }
  }, [guideOpen, wide, path]);
  const flip = () => { const t = theme === "dark" ? "light" : "dark"; setTheme(t); setThemeState(t); };
  const side = area !== null && path !== "/kit";

  return (
    <ExplainerContext.Provider value={setExplainer}>
      <a href="#main" className="skip-link" onClick={(e) => { e.preventDefault(); document.getElementById("main")?.focus(); }}>Skip to the page</a>
      <header className="top">
        <div className="top-in">
          <a className="brand" href="/" onClick={(e) => { e.preventDefault(); navigate("/"); }} aria-label="Launchway home">
            <Icon name="logo" size="xl" /><span className="bn">Launchway</span>
          </a>
          <Route ctx={ctx} on={area} needs={badN + warnN} />
          <DoBarButton onOpen={() => setLayer("do")} />
          <div className="top-r">
            <button type="button" className="iconbtn" onClick={() => setLayer("areas")} aria-label="All areas and what each is for" title="All areas" data-act="areas"><Icon name="apps" /></button>
            <a className={"btn btn-sm" + (isLearn(path) ? " on" : "")} href="/learn" onClick={onLink} aria-label={`${terms().label("learn")}: the documentation of this release`} aria-current={isLearn(path) ? "page" : undefined} data-act="learn"><Icon name="book" size="s" /><span className="hide-s">{terms().say("{Learn}")}</span></a>
            <button type="button" className={"btn btn-sm" + (guideOpen ? " on" : "")} onClick={toggleGuide} aria-pressed={guideOpen} aria-label={terms().label("guide")} data-act="guide"><Icon name="info" size="s" /><span className="hide-s">{terms().label("guide")}</span></button>
            <button type="button" className="iconbtn" onClick={flip} aria-label={theme === "dark" ? "Switch to light" : "Switch to dark"}><Icon name={theme === "dark" ? "sun" : "moon"} /></button>
            <UserMenu me={me} />
          </div>
        </div>
        <Pulse state={pulse} />
      </header>
      <div className={"shell" + (side && hasSide(ctx, area) ? " has-side" : "") + (guideOpen ? " rail-open" : "")}>
        <SideNav ctx={ctx} area={side ? area : null} path={path} />
        <main className="main" id="main" tabIndex={-1}>
          {me.auth_mode === "none" && (
            <div className="notice" role="note"><strong>No sign-in:</strong> anyone who can reach this server has full access.</div>
          )}
          <div className="frame-head"><Breadcrumbs path={path} label={crumb} /></div>
          <CrumbLabelContext.Provider value={setCrumb}>
            <Suspense fallback={<div className="page"><p className="sub">Loading…</p></div>}>
              <ErrorBoundary name="This page" resetKey={path}><PageFor path={path} approvals={approvals} /></ErrorBoundary>
            </Suspense>
          </CrumbLabelContext.Provider>
          {version && <footer className="foot faint small">Launchway {version}</footer>}
        </main>
        {guideOpen && <Suspense fallback={<aside className="rail" aria-label={terms().label("guide")} />}><Guide ctx={ctx} path={path} explainer={explainer} onClose={toggleGuide} focusOnOpen={!wide} /></Suspense>}
        {guideOpen && <div className="rail-back" onClick={toggleGuide} aria-hidden="true" />}
      </div>
      <TabBar ctx={ctx} on={area} needs={badN + warnN} onAreas={() => setLayer("areas")} />
      {layer === "do" && <Suspense fallback={null}><DoBar ctx={ctx} projects={inst.projects} onClose={() => setLayer(null)} /></Suspense>}
      {layer === "keys" && <Suspense fallback={null}><Shortcuts onClose={() => setLayer(null)} /></Suspense>}
      {layer === "areas" && <Launcher ctx={ctx} onClose={() => setLayer(null)} />}
    </ExplainerContext.Provider>
  );
}

function hasSide(ctx: Ctx, area: SectionId | null): boolean {
  return area !== null && visibleTabs(section(area), ctx).length > 1;
}
