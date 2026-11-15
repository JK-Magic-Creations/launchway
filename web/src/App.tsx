import { useCallback, useEffect, useState } from "react";
import { api, ApiError, SIGNED_OUT, type Me } from "./api";
import { MeContext } from "./auth";
import { useApprovals } from "./live";
import { match, navigate, onLink, usePath } from "./router";
import { RunsPage } from "./pages/Runs";
import { RunPage } from "./pages/Run";
import { ProjectsPage } from "./pages/Projects";
import { RunnersPage } from "./pages/Runners";
import { PlaygroundPage } from "./pages/Playground";
import { ApprovalsPage } from "./pages/Approvals";
import { TokensPage } from "./pages/Tokens";
import { AuditPage } from "./pages/Audit";
import { EditPipelinePage } from "./pages/EditPipeline";
import { ProjectTestsPage } from "./pages/ProjectTests";
import { nextFromHere, SignInPage } from "./pages/SignIn";
import { ProjectSecurityPage } from "./pages/ProjectSecurity";
import { ComponentsPage } from "./pages/Components";
import { Empty, ErrorBox, Link } from "./components/ui";

interface NavItem { href: string; label: string; active: (p: string) => boolean; show?: (me: Me) => boolean }

const NAV: NavItem[] = [
  { href: "/", label: "Runs", active: (p) => p === "/" || p.startsWith("/runs") },
  { href: "/approvals", label: "Approvals", active: (p) => p.startsWith("/approvals") },
  { href: "/projects", label: "Projects", active: (p) => p.startsWith("/projects") },
  { href: "/runners", label: "Runners", active: (p) => p.startsWith("/runners"), show: (me) => me.runners },
  { href: "/playground", label: "Playground", active: (p) => p.startsWith("/playground"), show: (me) => me.runners },
  { href: "/tokens", label: "API tokens", active: (p) => p.startsWith("/tokens"), show: (me) => me.auth_mode === "oidc" },
  { href: "/audit", label: "Audit trail", active: (p) => p.startsWith("/audit"), show: (me) => me.admin },
  { href: "/components", label: "Components", active: (p) => p.startsWith("/components") },
];

// Shown so people can see where this is going; none of these work yet.
const NEXT = ["Environments", "Releases", "Infrastructure", "Policy"];

type Who = { state: "loading" } | { state: "out"; next: string } | { state: "in"; me: Me } | { state: "error"; error: unknown };

// App finds out who is signed in first. Anyone who is not, or whose session
// ends while they use the page, sees the sign-in page and comes back to
// where they were afterwards.
export function App() {
  const [who, setWho] = useState<Who>({ state: "loading" });
  const loadMe = useCallback(() => {
    api.me().then((me) => setWho({ state: "in", me })).catch((e) => {
      if (e instanceof ApiError && e.status === 401) setWho({ state: "out", next: nextFromHere() });
      else setWho({ state: "error", error: e });
    });
  }, []);
  useEffect(loadMe, [loadMe]);
  useEffect(() => {
    const out = () => setWho((w) => (w.state === "out" ? w : { state: "out", next: nextFromHere() }));
    window.addEventListener(SIGNED_OUT, out);
    return () => window.removeEventListener(SIGNED_OUT, out);
  }, []);

  if (who.state === "loading") return <div className="page"><div className="loading">Loading…</div></div>;
  if (who.state === "error") return <div className="page"><ErrorBox error={who.error} /></div>;
  if (who.state === "out") return <SignInPage next={who.next} />;
  return (
    <MeContext.Provider value={who.me}>
      <Shell me={who.me} />
    </MeContext.Provider>
  );
}

function Shell({ me }: { me: Me }) {
  const path = usePath();
  const [version, setVersion] = useState("");
  const approvals = useApprovals();
  const waiting = approvals.list?.length ?? 0;
  useEffect(() => { api.info().then((i) => setVersion(i.version)).catch(() => {}); }, []);
  // Signed in already: the sign-in page has nothing to offer.
  useEffect(() => { if (path === "/signin") navigate("/"); }, [path]);

  const nav = NAV.filter((n) => !n.show || n.show(me));
  let page;
  let m;
  if (path === "/") page = <RunsPage />;
  else if ((m = match("/runs/:id", path))) page = <RunPage id={m.id} />;
  else if (path === "/approvals") page = <ApprovalsPage approvals={approvals} />;
  else if (path === "/projects") page = <ProjectsPage />;
  else if ((m = match("/projects/:name/pipeline", path))) page = <EditPipelinePage key={m.name} name={m.name} />;
  else if ((m = match("/projects/:name/tests", path))) page = <ProjectTestsPage key={m.name} name={m.name} />;
  else if (path === "/runners") page = <RunnersPage />;
  else if (path === "/playground") page = <PlaygroundPage />;
  else if (path === "/tokens") page = <TokensPage />;
  else if (path === "/audit") page = <AuditPage />;
  else if ((m = match("/projects/:name/security", path))) page = <ProjectSecurityPage key={m.name} name={m.name} />;
  else if (path === "/components") page = <ComponentsPage />;
  else page = <div className="page"><Empty title="Nothing here">This page does not exist. <Link href="/">Go to runs</Link>.</Empty></div>;

  useEffect(() => {
    const title = NAV.find((n) => n.active(path))?.label;
    document.title = title ? `${title} · Launchway` : "Launchway";
  }, [path]);

  return (
    <div className="shell">
      <nav className="nav" aria-label="Main">
        <a href="/" onClick={onLink} className="brand">
          <svg width="26" height="26" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#2563eb" /><path d="M8 22l8-12 8 12" stroke="#fff" strokeWidth="3" fill="none" strokeLinecap="round" strokeLinejoin="round" /></svg>
          <span>Launchway</span>
        </a>
        <div className="nav-links">
          {nav.map((n) => (
            <a key={n.href} href={n.href} onClick={onLink} className={"nav-link" + (n.active(path) ? " nav-on" : "")}
              aria-current={n.active(path) ? "page" : undefined}>
              {n.label}
              {n.href === "/approvals" && waiting > 0 && (
                <span className="nav-badge" aria-label={`${waiting} waiting`}>{waiting}</span>
              )}
            </a>
          ))}
        </div>
        <div className="nav-next">
          <div className="nav-next-title">Coming next</div>
          {NEXT.map((n) => <div key={n} className="nav-soon" title="Designed, not built yet">{n}</div>)}
        </div>
        <div className="nav-foot">
          <UserMenu me={me} />
          {version && <span>Launchway {version}</span>}
        </div>
      </nav>
      <main className="main">
        {me.auth_mode === "none" && (
          <div className="notice" role="note">
            <strong>No sign-in:</strong> anyone who can reach this server has full access.
          </div>
        )}
        {page}
      </main>
    </div>
  );
}

// UserMenu shows who is signed in, their groups, and a way to sign out.
function UserMenu({ me }: { me: Me }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const signOut = async () => {
    setBusy(true); setError(false);
    try {
      await api.logout();
      location.assign("/signin");
    } catch { setError(true); setBusy(false); }
  };
  return (
    <details className="usermenu">
      <summary>
        <span className="usermenu-avatar" aria-hidden="true">{me.user.slice(0, 1).toUpperCase()}</span>
        <span className="usermenu-name">{me.user}</span>
      </summary>
      <div className="usermenu-body">
        {me.auth_mode === "none" ? (
          <p>Everyone acts as <strong>{me.user}</strong>, the server's configured user.</p>
        ) : (
          <>
            <div className="usermenu-title">Groups</div>
            {me.groups.length ? (
              <ul className="usermenu-groups">{me.groups.map((g) => <li key={g}>{g}</li>)}</ul>
            ) : <p>None</p>}
            <button type="button" className="btn btn-sm usermenu-out" disabled={busy} onClick={signOut}>{busy ? "Signing out…" : "Sign out"}</button>
            {error && <p role="alert">Signing out did not work. Try again.</p>}
          </>
        )}
      </div>
    </details>
  );
}
