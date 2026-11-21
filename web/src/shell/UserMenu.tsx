import { useEffect, useRef, useState } from "react";
import { api, type Me } from "../api";
import { setShortcut, shortcutOn } from "./prefs";

// UserMenu shows who is signed in, their groups, and a way to sign out.
export function UserMenu({ me }: { me: Me }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [shortcut, setShortcutState] = useState(shortcutOn);
  const signOut = async () => {
    setBusy(true); setError(false);
    try {
      await api.logout();
      location.assign("/signin");
    } catch { setError(true); setBusy(false); }
  };
  // Escape or a click elsewhere closes the menu, and Escape gives focus back to its button.
  const box = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const key = (e: KeyboardEvent) => { if (e.key === "Escape" && el.open) { el.open = false; el.querySelector("summary")?.focus(); } };
    const away = (e: MouseEvent) => { if (el.open && !el.contains(e.target as Node)) el.open = false; };
    document.addEventListener("keydown", key); document.addEventListener("mousedown", away);
    return () => { document.removeEventListener("keydown", key); document.removeEventListener("mousedown", away); };
  }, []);
  return (
    <details className="usermenu" ref={box}>
      <summary aria-label={`Signed in as ${me.user}`}>
        <span className="avatar" aria-hidden="true">{me.user.slice(0, 1).toUpperCase()}</span>
      </summary>
      <div className="usermenu-body">
        <div className="usermenu-title">{me.user}</div>
        <label className="usermenu-pref">
          <input type="checkbox" checked={shortcut} onChange={(e) => { setShortcut(e.target.checked); setShortcutState(e.target.checked); }} />
          <span>Press <kbd>/</kbd> to search</span>
        </label>
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
