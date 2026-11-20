import { Suspense, useCallback, useEffect, useState } from "react";
import { api, ApiError, SIGNED_OUT, type Me } from "./api";
import { MeContext } from "./auth";
import { useApprovals, type Approvals } from "./live";
import { navigate, onLink, replace, usePath } from "./router";
import { nextFromHere, SignInPage } from "./pages/SignIn";
import { ErrorBox } from "./components/ui";
import { InstallationProvider, useInstallation } from "./guided/facts";
import { Breadcrumbs, SectionLinks, SectionTabs } from "./nav/Navigation";
import { CrumbLabelContext } from "./nav/crumbs";
import { pageTitle, redirectFor } from "./nav/model";
import { PageFor } from "./nav/pages";


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
  const approvals = useApprovals();
  return (
    <InstallationProvider me={me} approvals={approvals.list ?? []}>
      <Frame me={me} approvals={approvals} />
    </InstallationProvider>
  );
}

function Frame({ me, approvals }: { me: Me; approvals: Approvals }) {
  const path = usePath();
  const inst = useInstallation();
  const [version, setVersion] = useState("");
  const [open, setOpen] = useState(false);
  const [crumb, setCrumb] = useState<string | undefined>();
  useEffect(() => { api.info().then((i) => setVersion(i.version)).catch(() => {}); }, []);
  // Signed in already: the sign-in page has nothing to offer.
  useEffect(() => { if (path === "/signin") navigate("/"); }, [path]);
  // An address from the old flat menu goes to where the page lives now.
  useEffect(() => {
    const to = redirectFor(path, location.search);
    if (to) replace(to);
  }, [path]);
  useEffect(() => { document.title = pageTitle(path); }, [path]);

  const ctx = { me, facts: inst.facts };
  return (
    <div className="shell">
      <a href="#main" className="skip">Skip to the page</a>
      <header className="topbar">
        <button type="button" className="btn btn-sm" aria-expanded={open} aria-controls="menu" onClick={() => setOpen((o) => !o)}>Menu</button>
        <strong>Launchway</strong>
      </header>
      <nav className={"nav" + (open ? " nav-open" : "")} id="menu" aria-label="Main">
        <a href="/" onClick={onLink} className="brand">
          <svg width="26" height="26" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#2563eb" /><path d="M8 22l8-12 8 12" stroke="#fff" strokeWidth="3" fill="none" strokeLinecap="round" strokeLinejoin="round" /></svg>
          <span>Launchway</span>
        </a>
        <SectionLinks ctx={ctx} path={path} onNavigate={() => setOpen(false)} />
        <div className="nav-foot">
          <UserMenu me={me} />
          {version && <span>Launchway {version}</span>}
        </div>
      </nav>
      <main className="main" id="main" tabIndex={-1}>
        {me.auth_mode === "none" && (
          <div className="notice" role="note">
            <strong>No sign-in:</strong> anyone who can reach this server has full access.
          </div>
        )}
        <div className="frame-head">
          <Breadcrumbs path={path} label={crumb} />
          <SectionTabs ctx={ctx} path={path} />
        </div>
        <CrumbLabelContext.Provider value={setCrumb}>
          <Suspense fallback={<div className="page"><p className="sub">Loading…</p></div>}>
            <PageFor path={path} approvals={approvals} />
          </Suspense>
        </CrumbLabelContext.Provider>
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
