import { useMe } from "../auth";
import { Link } from "../components/ui";
import { PageHead } from "../kit/layout";

/** People and access: how people and programs prove who they are. Each choice that is on is a card. */
export function AccessPage() {
  const me = useMe();
  if (!me) return null;
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="People and access"
        sentence={<>Signed in as <strong>{me.user}</strong>{me.groups.length > 0 ? `, in ${me.groups.join(", ")}` : ""}. {me.auth_mode === "none" ? "Sign-in is off, so everyone acts as this user." : "Groups decide what each person may do."}</>} />
      <ul className="guided-tiles guided-tiles-wide">
        {me.auth_mode === "oidc" && (
          <li><Link href="/settings/access/keys" className="guided-tile guided-tile-block"><strong>Keys for scripts</strong><span className="faint">A key lets a script or the command line act as you, for as long as you choose.</span></Link></li>
        )}
        {me.mcp?.enabled !== false && (
          <li><Link href="/settings/access/agents" className="guided-tile guided-tile-block"><strong>Connect an agent</strong><span className="faint">Let an AI assistant read your runs and, if you allow it, act on them.</span></Link></li>
        )}
        {me.admin && me.auth_mode === "none" && (
          <li><Link href="/config/wizard/first-run?step=signin" className="guided-tile guided-tile-block"><strong>Turn on sign-in</strong><span className="faint">So people use the accounts they have, and groups decide what each may do.</span></Link></li>
        )}
      </ul>
    </div>
  );
}
