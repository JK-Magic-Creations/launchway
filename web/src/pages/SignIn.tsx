import { useEffect } from "react";

const ERRORS: Record<string, string> = {
  expired: "The sign-in took too long or was started in another browser. Please try again.",
  state: "The sign-in could not be matched to this browser. Please try again.",
  provider: "The sign-in provider did not sign you in.",
  failed: "The sign-in provider's answer could not be accepted. If this keeps happening, ask your administrator to look at the audit trail.",
  unavailable: "Launchway could not record the sign-in. Please try again shortly.",
};

// The page shown to anyone not signed in. `next` is where to go afterwards.
export function SignInPage({ next }: { next: string }) {
  const error = new URLSearchParams(location.search).get("error");
  useEffect(() => { document.title = "Sign in · Launchway"; }, []);
  const href = "/auth/login?next=" + encodeURIComponent(next);
  return (
    <main className="signin">
      <div className="card signin-card">
        <div className="signin-brand">
          <svg width="40" height="40" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#2563eb" /><path d="M8 22l8-12 8 12" stroke="#fff" strokeWidth="3" fill="none" strokeLinecap="round" strokeLinejoin="round" /></svg>
          <h1>Launchway</h1>
        </div>
        <p className="sub">Sign in with your organisation's account to see and run pipelines.</p>
        {error && <div className="errorbox" role="alert">{ERRORS[error] ?? "The sign-in did not work. Please try again."}</div>}
        <a className="btn btn-primary btn-wide" href={href}>Sign in with single sign-on</a>
        <p className="hint signin-learn">New to Launchway? <a href="/learn/start-here">Read how it works</a>, no sign-in needed.</p>
      </div>
    </main>
  );
}

/** Where to send someone after they sign in, from where they are now. */
export function nextFromHere(): string {
  if (location.pathname === "/signin") {
    const n = new URLSearchParams(location.search).get("next");
    return n && n.startsWith("/") && !n.startsWith("//") ? n : "/";
  }
  return location.pathname + location.search;
}
