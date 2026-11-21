import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { call, ApiError } from "../api";
import { QrCode } from "../kit/QrCode";

// Built-in accounts: sign-in with a second step, first-run setup with the one-time code, and the page a
// one-time link opens. Loaded only by someone who is not signed in (see SignIn.tsx and App.tsx).

interface Begin { pending: string; totp_secret: string; totp_uri: string; second_factor_required: boolean; username: string }
interface Done { signed_in: boolean; recovery_codes?: string[]; next?: string }

function Shell({ title, sub, children }: { title: string; sub: string; children: ReactNode }) {
  useEffect(() => { document.title = title + " · Launchway"; }, [title]);
  return (
    <main className="signin">
      <div className="card signin-card">
        <div className="signin-brand">
          <svg width="40" height="40" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#2563eb" /><path d="M8 22l8-12 8 12" stroke="#fff" strokeWidth="3" fill="none" strokeLinecap="round" strokeLinejoin="round" /></svg>
          <h1>{title}</h1>
        </div>
        <p className="sub">{sub}</p>
        {children}
      </div>
    </main>
  );
}

function Err({ error }: { error: string }) {
  return error ? <div className="errorbox" role="alert">{error}</div> : null;
}

function message(e: unknown): string {
  return e instanceof ApiError ? e.message : "Launchway could not be reached. Try again shortly.";
}

/** The password form and, when the account has one, the code step. */
export function LocalSignIn({ next }: { next: string }) {
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [ticket, setTicket] = useState("");
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const go = (r: { signed_in?: boolean; ticket?: string }) => {
    if (r.signed_in) location.assign(next);
    else if (r.ticket) { setTicket(r.ticket); setError(""); }
  };
  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    setBusy(true); setError("");
    try {
      if (!ticket) go(await call("POST", "/api/v1/auth/local/signin", { username: user, password }));
      else go(await call("POST", "/api/v1/auth/local/second-factor", recovery ? { ticket, recovery_code: code } : { ticket, code }));
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return (
    <form onSubmit={submit} className="stack">
      <Err error={error} />
      {!ticket ? (
        <>
          <label className="field"><span>User name</span>
            <input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="username" required /></label>
          <label className="field"><span>Password</span>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></label>
        </>
      ) : (
        <label className="field"><span>{recovery ? "Recovery code" : "Code from your authenticator app"}</span>
          <input value={code} onChange={(e) => setCode(e.target.value)} inputMode={recovery ? "text" : "numeric"} autoComplete="one-time-code" required /></label>
      )}
      <button className="btn btn-primary btn-wide" disabled={busy}>{ticket ? "Verify" : "Sign in"}</button>
      {ticket && <button type="button" className="btn btn-wide" onClick={() => { setRecovery(!recovery); setCode(""); }}>{recovery ? "Use my authenticator app instead" : "Use a recovery code instead"}</button>}
    </form>
  );
}

/** Choose a password and set up the authenticator; shared by first-run setup and one-time links. */
function Enroll({ begin, extra, complete, after = "/" }: { begin: string; extra: (b: Record<string, string>) => Record<string, string>; complete: string; after?: string }) {
  const [form, setForm] = useState<Record<string, string>>({});
  const [step, setStep] = useState<Begin | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (k: string) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  const first = async (ev: FormEvent) => {
    ev.preventDefault(); setBusy(true); setError("");
    try {
      const b = await call<Begin>("POST", begin, extra(form));
      if (b.second_factor_required) setStep(b); else await finish(b, "");
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  const finish = async (b: Begin, c: string) => {
    const d = await call<Done>("POST", complete, { pending: b.pending, code: c });
    if (d.recovery_codes?.length) setCodes(d.recovery_codes); else location.assign(after);
  };
  const second = async (ev: FormEvent) => {
    ev.preventDefault(); if (!step) return; setBusy(true); setError("");
    try { await finish(step, code); } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };

  if (codes) {
    return (
      <div className="stack">
        <p>You are signed in. Save these recovery codes somewhere safe, such as a password manager. Each works once if you lose your authenticator app. They are not shown again.</p>
        <ul className="codes" aria-label="Recovery codes">{codes.map((c) => <li key={c}><code>{c}</code></li>)}</ul>
        <button className="btn btn-primary btn-wide" onClick={() => location.assign(after)}>I have saved them, continue</button>
      </div>
    );
  }
  if (step) {
    return (
      <form onSubmit={second} className="stack">
        <Err error={error} />
        <p>Scan this with an authenticator app (any app that makes six-digit codes), or choose to enter a setup key and type it in.</p>
        <div className="qr-box"><QrCode text={step.totp_uri} label="QR code for your authenticator app. The same secret is written below as a setup key." /></div>
        <p><code aria-label="Setup key">{step.totp_secret}</code></p>
        <p className="hint">On the device that has the app, you can also open <a href={step.totp_uri}>this address</a>.</p>
        <label className="field"><span>The six-digit code it shows now</span>
          <input value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" autoComplete="one-time-code" required /></label>
        <button className="btn btn-primary btn-wide" disabled={busy}>Finish</button>
      </form>
    );
  }
  return (
    <form onSubmit={first} className="stack">
      <Err error={error} />
      {"code" in extra({}) && (
        <label className="field"><span>Setup code</span>
          <input value={form.code ?? ""} onChange={set("code")} autoComplete="off" autoCapitalize="characters" required /></label>
      )}
      {"username" in extra({}) && (
        <>
          <label className="field"><span>Your name</span><input value={form.display_name ?? ""} onChange={set("display_name")} autoComplete="name" /></label>
          <label className="field"><span>User name</span><input value={form.username ?? ""} onChange={set("username")} autoComplete="username" required /></label>
        </>
      )}
      <label className="field"><span>Password</span>
        <input type="password" value={form.password ?? ""} onChange={set("password")} autoComplete="new-password" required aria-describedby="pw-hint" /></label>
      <p id="pw-hint" className="hint">At least 12 characters. A few unrelated words work well.</p>
      <button className="btn btn-primary btn-wide" disabled={busy}>Continue</button>
    </form>
  );
}

/** "Set up this installation": the one-time setup code, then the first administrator. */
export function SetupPage() {
  return (
    <Shell title="Set up this installation" sub="Enter the one-time setup code from the installation notes (or run `launchway users setup-code` on the server), then create the first administrator.">
      <Enroll begin="/api/v1/setup/begin" complete="/api/v1/setup/complete" after="/setup/guide"
        extra={(f) => ({ code: f.code ?? "", username: f.username ?? "", display_name: f.display_name ?? "", password: f.password ?? "" })} />
    </Shell>
  );
}

/** What a one-time link (an invitation or a reset) opens. */
export function AcceptPage() {
  const token = new URLSearchParams(location.search).get("token") ?? "";
  const [info, setInfo] = useState<{ username: string; kind: string } | "bad" | null>(null);
  useEffect(() => { call<{ username: string; kind: string }>("POST", "/api/v1/auth/link", { token }).then(setInfo).catch(() => setInfo("bad")); }, [token]);
  if (info === null) return <Shell title="One-time link" sub="Checking the link."><p className="loading">Checking…</p></Shell>;
  if (info === "bad") return <Shell title="This link does not work" sub="It was already used or has expired."><p>Ask an administrator for a new link, or run <code>launchway users reset NAME</code> on the server.</p></Shell>;
  return (
    <Shell title={info.kind === "reset" ? "Choose a new password" : "Welcome to Launchway"} sub={`Account: ${info.username}`}>
      <Enroll begin="/api/v1/auth/link/begin" complete="/api/v1/auth/link/complete" extra={(f) => ({ token, password: f.password ?? "" })} />
    </Shell>
  );
}
