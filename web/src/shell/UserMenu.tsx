import { useState } from "react";
import { api, type Me } from "../api";

// UserMenu shows who is signed in, their groups, and a way to sign out.
export function UserMenu({ me }: { me: Me }) {
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
      <summary aria-label={`Signed in as ${me.user}`}>
        <span className="avatar" aria-hidden="true">{me.user.slice(0, 1).toUpperCase()}</span>
      </summary>
      <div className="usermenu-body">
        <div className="usermenu-title">{me.user}</div>
        {me.auth_mode === "none" ? (
          <p>Everyone acts as <strong>{me.user}</strong>, the server's configured user.</p>
        ) : (
          <>
            <div className="eyebrow">Groups</div>
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
