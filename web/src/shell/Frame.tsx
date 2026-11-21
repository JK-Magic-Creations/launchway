import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { api, type Approval, type Me } from "../api";
import { attention } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { Icon } from "../kit/Icon";
import { CrumbLabelContext } from "../nav/crumbs";
import { Breadcrumbs } from "../nav/Navigation";
import { pageTitle, redirectFor, section, sectionOf, visibleTabs, type Ctx, type SectionId } from "../nav/model";
import { PageFor } from "../nav/pages";
import type { Approvals } from "../live";
import { navigate, replace, usePath } from "../router";
import { DoBar, DoBarButton } from "./DoBar";
import { ExplainerContext, Guide, type Explainer } from "./Guide";
import { Launcher } from "./Launcher";
import { Pulse, Route, TabBar, type PulseState } from "./Route";
import { SideNav } from "./SideNav";
import { currentTheme, setTheme } from "./theme";
import { UserMenu } from "./UserMenu";
import { terms, useTerms } from "../terms";

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
  if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return false;
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
  const [layer, setLayer] = useState<"do" | "areas" | null>(null);
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
  useEffect(() => { document.title = pageTitle(path); }, [path]);
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (opensDoBar(e)) { e.preventDefault(); setLayer("do"); } };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, []);

  const ctx = { me, facts: inst.facts };
  const area = sectionOf(path);
  const needs = useMemo(() => attention({ projects: inst.projects, approvals: approvals.list ?? ([] as Approval[]), runners: inst.runners, applications: inst.applications, me }),
    [inst.projects, approvals.list, inst.runners, inst.applications, me]);
  const pulse = pulseOf(needs.filter((n) => n.tone === "bad").length, needs.filter((n) => n.tone === "warn").length, inst.runs.some((r) => r.status === "running"));
  // Wide screens remember the choice, and open the Guide on Home. A narrow screen shows it as a sheet, closed until asked.
  const wide = useWide();
  const [sheetAt, setSheetAt] = useState<string | null>(null); // the page the sheet was opened on: it closes on the next page
  const sheet = sheetAt === path;
  const guideOpen = wide ? (guidePref ?? path === "/") : sheet;
  const toggleGuide = useCallback(() => {
    const next = !guideOpen;
    if (!wide) { setSheetAt(next ? path : null); return; }
    setGuidePref(next);
    try { localStorage.setItem(GUIDE_KEY, next ? "1" : "0"); } catch { /* storage may be blocked */ }
  }, [guideOpen, wide, path]);
  const flip = () => { const t = theme === "dark" ? "light" : "dark"; setTheme(t); setThemeState(t); };
  const side = area !== null && path !== "/kit";

  return (
    <ExplainerContext.Provider value={setExplainer}>
      <a href="#main" className="skip-link">Skip to the page</a>
      <header className="top">
        <div className="top-in">
          <a className="brand" href="/" onClick={(e) => { e.preventDefault(); navigate("/"); }} aria-label="Launchway home">
            <Icon name="logo" size="xl" /><span className="bn">Launchway</span>
          </a>
          <Route ctx={ctx} on={area} needs={needs.length} />
          <DoBarButton onOpen={() => setLayer("do")} />
          <div className="top-r">
            <button type="button" className="iconbtn" onClick={() => setLayer("areas")} aria-label="All areas and what each is for" title="All areas" data-act="areas"><Icon name="apps" /></button>
            <button type="button" className={"btn btn-sm" + (guideOpen ? " on" : "")} onClick={toggleGuide} aria-pressed={guideOpen} aria-label={terms().label("guide")}><Icon name="info" size="s" /><span className="hide-s">{terms().label("guide")}</span></button>
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
              <PageFor path={path} approvals={approvals} />
            </Suspense>
          </CrumbLabelContext.Provider>
          {version && <footer className="foot faint small">Launchway {version}</footer>}
        </main>
        {guideOpen && <Guide ctx={ctx} path={path} explainer={explainer} onClose={toggleGuide} />}
        {guideOpen && <div className="rail-back" onClick={toggleGuide} aria-hidden="true" />}
      </div>
      <TabBar ctx={ctx} on={area} needs={needs.length} onAreas={() => setLayer("areas")} />
      {layer === "do" && <DoBar ctx={ctx} projects={inst.projects} onClose={() => setLayer(null)} />}
      {layer === "areas" && <Launcher ctx={ctx} onClose={() => setLayer(null)} />}
    </ExplainerContext.Provider>
  );
}

function hasSide(ctx: Ctx, area: SectionId | null): boolean {
  return area !== null && visibleTabs(section(area), ctx).length > 1;
}
