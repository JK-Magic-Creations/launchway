import { useEffect, useRef, useState } from "react";
import { ApiError } from "../../api";
import { configApi, type SignInCheck } from "../../configApi";
import { actionOf, guideApi, type GuideStep, type SigninTestResult } from "../../guideApi";
import { Button } from "../../kit/layout";
import { Unavailable, Wingman } from "./Frame";

type Phase = "idle" | "starting" | "waiting" | "worked" | "failed";

/** Step 3: keep built-in accounts, or connect an identity provider, tested before anything is switched. The test is a
 * real sign-in in a second window; the built-in administrator stays as the way back in. */
export function SignInStep({ step, onChange }: { step: GuideStep; onChange: () => void }) {
  const [open, setOpen] = useState(false);
  const [issuer, setIssuer] = useState("");
  const [clientId, setClientId] = useState("launchway");
  const [secretEnv, setSecretEnv] = useState("LAUNCHWAY_OIDC_SECRET");
  const [groups, setGroups] = useState("");
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState("");
  const [result, setResult] = useState<SigninTestResult | null>(null);
  const [snippet, setSnippet] = useState<SignInCheck | null>(null);
  const [url, setUrl] = useState("");
  const started = useRef(0);
  const test = actionOf(step, "test_provider");
  const keep = actionOf(step, "keep_built_in");
  const sw = actionOf(step, "switch_provider_on");

  useEffect(() => {
    if (phase !== "waiting") return;
    const t = window.setInterval(async () => {
      try {
        const r = await guideApi.signinResult();
        if (r.tested && (r.at ?? 0) >= started.current) {
          setResult(r);
          setPhase(r.ok ? "worked" : "failed");
          if (r.ok) {
            try { setSnippet(await configApi.checkSignIn({ issuer: issuer.trim(), client_id: clientId.trim(), redirect_url: `${location.origin}/auth/callback`, client_secret_env: secretEnv.trim(),
              groups_claim: groups.trim() || undefined })); } catch { /* the snippet is a convenience */ }
          }
        }
      } catch { /* keep waiting */ }
    }, 2000);
    return () => window.clearInterval(t);
  }, [phase, issuer, clientId, secretEnv, groups]);

  const begin = async () => {
    setPhase("starting"); setError(""); setResult(null); setSnippet(null);
    started.current = Date.now() - 2000;
    try {
      const b = await guideApi.testSignin({ issuer: issuer.trim(), client_id: clientId.trim(), client_secret_env: secretEnv.trim(), groups_claim: groups.trim() || undefined });
      const w = window.open(b.url, "launchway-signin-test");
      if (!w) { setError("The browser blocked the second window. Allow pop-ups for this page, or open the address below yourself."); }
      setPhase("waiting");
      setUrl(b.url);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message}` : "Launchway could not be reached. Try again shortly.");
      setPhase("idle");
    }
  };
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about sign-in and access", slug: "guide/access" }}>
        Built-in accounts (a password and an authenticator) are a complete, secure choice, and the way back in if a provider ever breaks. Connect an identity provider only
        if your organization has one. I test it with a real sign-in in a second window first, and nothing about how people sign in changes until you decide.
      </Wingman>
      <div className="gd-choices">
        <Button kind="pri" disabled={!test?.available} onClick={() => setOpen(!open)} aria-expanded={open}>Connect an identity provider</Button>
        {keep?.available && <Button onClick={onChange}>Keep built-in accounts</Button>}
      </div>
      <Unavailable action={test} />
      {open && test?.available && (
        <form className="gd-form" onSubmit={(e) => { e.preventDefault(); void begin(); }} aria-label="Test an identity provider">
          <p className="faint">First make an application (an OpenID Connect client) for Launchway in your provider. Its redirect address for the test is <code>{location.origin}/auth/signin-test</code>; for real use it is <code>{location.origin}/auth/callback</code>.</p>
          <label className="field"><span>The provider's address (issuer)</span><input value={issuer} onChange={(e) => setIssuer(e.target.value)} placeholder="https://sso.example.com/realms/acme" required /></label>
          <label className="field"><span>Client id</span><input value={clientId} onChange={(e) => setClientId(e.target.value)} required /></label>
          <label className="field"><span>Name of the environment variable that holds the client secret</span><input value={secretEnv} onChange={(e) => setSecretEnv(e.target.value)} aria-describedby="gd-secret-hint" />
            <small id="gd-secret-hint">The secret is never typed here: put it in this variable on every server.</small></label>
          <label className="field"><span>Groups claim (if your provider uses another name than groups)</span><input value={groups} onChange={(e) => setGroups(e.target.value)} /></label>
          <div><button type="submit" className="btn btn-primary" disabled={phase === "starting" || phase === "waiting"}>{phase === "starting" ? "Starting…" : "Test the sign-in in a second window"}</button></div>
        </form>
      )}
      <div aria-live="polite">
        {error && <div className="callout callout-bad" role="alert">{error}</div>}
        {phase === "waiting" && <div className="callout callout-info">Waiting for you to sign in at the provider in the second window. {url && <>If it did not open, <a href={url} target="_blank" rel="noreferrer">open it here</a>.</>}</div>}
        {phase === "failed" && <div className="callout callout-bad">The test did not work: {result?.detail}. Fix what it says and test again; nothing has changed.</div>}
        {phase === "worked" && (
          <div className="callout callout-ok">
            <strong>The test worked.</strong> {result?.detail}. Nothing about how people sign in has changed yet, and the built-in administrator stays as the way back in.
          </div>
        )}
      </div>
      {phase === "worked" && (
        <div className="gd-next">
          <h3 className="h-sm">To switch it on</h3>
          <Unavailable action={sw} />
          {snippet?.snippet && <><p>Add this to the server's settings file, or to the chart's <code>config.extraConfig</code>, and restart:</p><textarea className="gd-code" readOnly rows={Math.min(14, snippet.snippet.split("\n").length + 1)} aria-label="The setting to add" value={snippet.snippet} /></>}
          <p className="faint">Built-in accounts can stay on beside the provider, so a mistake in the provider's settings cannot lock everyone out.</p>
        </div>
      )}
    </>
  );
}
