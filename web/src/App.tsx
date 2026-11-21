import { useCallback, useEffect, useState } from "react";
import { api, ApiError, SIGNED_OUT, type Me } from "./api";
import { MeContext } from "./auth";
import { useApprovals } from "./live";
import { nextFromHere, SignInPage } from "./pages/SignIn";
import { ErrorBox } from "./components/ui";
import { InstallationProvider } from "./guided/facts";
import { Frame } from "./shell/Frame";


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
