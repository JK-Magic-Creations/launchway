import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, SIGNED_OUT, type Me } from "./api";
import { MeContext } from "./auth";
import { Announcer, announce } from "./kit/announce";
import { useApprovals } from "./live";
import { nextFromHere, SignInPage } from "./pages/SignIn";
import { useOverview } from "./configApi";
import { ErrorBox } from "./components/ui";
import { InstallationProvider } from "./guided/facts";
import { Frame } from "./shell/Frame";
import { isLearn } from "./nav/model";


// Loaded only when someone not signed in opens Learn: the first page needs none of it.
const PublicLearn = lazy(() => import("./learn/PublicLearn").then((m) => ({ default: m.PublicLearn })));

type Who = { state: "loading" } | { state: "out"; next: string } | { state: "in"; me: Me } | { state: "error"; error: unknown };

// App finds out who is signed in first. Anyone who is not, or whose session
// ends while they use the page, sees the sign-in page and comes back to
// where they were afterwards.
export function App() {
  return <><Announcer /><AppBody /></>;
}

function AppBody() {
  const [who, setWho] = useState<Who>({ state: "loading" });
  const whoState = useRef(who.state);
  useEffect(() => { whoState.current = who.state; }, [who.state]);
  const loadMe = useCallback(() => {
    api.me().then((me) => setWho({ state: "in", me })).catch((e) => {
      if (e instanceof ApiError && e.status === 401) setWho({ state: "out", next: nextFromHere() });
      else setWho({ state: "error", error: e });
    });
  }, []);
  useEffect(loadMe, [loadMe]);
  useEffect(() => {
    const out = () => {
      // Said once, outside the state update: a person using a screen reader hears that the session ended, not only sees a new page.
      if (whoState.current !== "out") announce("Your session ended. Sign in again to continue where you were.", "assertive");
      setWho((w) => (w.state === "out" ? w : { state: "out", next: nextFromHere() }));
    };
    window.addEventListener(SIGNED_OUT, out);
    return () => window.removeEventListener(SIGNED_OUT, out);
  }, []);

  if (who.state === "loading") return <div className="page"><div className="loading">Loading…</div></div>;
  if (who.state === "error") return <div className="page"><ErrorBox error={who.error} /></div>;
  // The documentation needs no sign-in: it is the public text of this release, and shows nothing of this installation.
  if (who.state === "out" && isLearn(location.pathname)) return <Suspense fallback={<div className="page"><div className="loading">Loading…</div></div>}><PublicLearn /></Suspense>;
  if (who.state === "out") return <SignInPage next={who.next} />;
  return (
    <MeContext.Provider value={who.me}>
      <Shell me={who.me} />
    </MeContext.Provider>
  );
}

function Shell({ me }: { me: Me }) {
  const approvals = useApprovals();
  // The words come with the settings: they are known before the first page is drawn.
  const { overview, error } = useOverview();
  if (!overview && !error) return <div className="page"><div className="loading">Loading…</div></div>;
  return (
    <InstallationProvider me={me} approvals={approvals.list ?? []}>
      <Frame me={me} approvals={approvals} />
    </InstallationProvider>
  );
}
