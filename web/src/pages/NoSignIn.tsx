import { useState } from "react";
import { api, type Me } from "../api";
import { ErrorBox, Mono } from "../components/ui";

/** Shown on every page while this installation runs with no sign-in on an address others can reach: it says so, and
 * offers "Set up sign-in now", which gives a one-time setup code (shown once) and asks the servers to start with
 * built-in accounts the next time they restart. Nothing changes until then. */
export function NoSignInBanner({ off }: { off: NonNullable<Me["sign_in_off"]> }) {
  const [code, setCode] = useState<string | null>(null);
  const [next, setNext] = useState<string>("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const ask = () => {
    setBusy(true);
    api.setupSignInNow().then((r) => { setCode(r.setup_code); setNext(r.next); }, setError).finally(() => setBusy(false));
  };
  return (
    <section className="notice" role="alert" aria-label="No sign-in">
      <strong>This installation has no sign-in.</strong> {off.warning.replace("This installation has no sign-in: ", "")}{" "}
      {code === null ? (
        <button type="button" className="btn" disabled={busy} onClick={ask}>Set up sign-in now</button>
      ) : (
        <>Your one-time setup code is <Mono>{code}</Mono>. {next}</>
      )}
      {off.requested_at && code === null && <> Sign-in was already asked for; it starts when the servers restart.</>}
      <ErrorBox error={error} />
    </section>
  );
}
