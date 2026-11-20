import { useMe } from "../auth";
import { Link } from "../components/ui";
import { useInstallation } from "../guided/facts";
import { section, visibleTabs } from "../nav/model";

const BLURB: Record<string, string> = {
  runners: "The machines that run your steps. Add one, see who is online, or take one away.",
  access: "Keys for scripts, and how to connect an AI assistant.",
  rules: "Rules every pipeline is held to before it deploys.",
  audit: "Who changed what, and when. Every sign-in and every change.",
  config: "Everything an administrator sets up, with a guided set-up for each, kept with its history.",
};

/** Settings: set up once. One card for each place the person is allowed to change things. */
export function SettingsPage() {
  const me = useMe();
  const inst = useInstallation();
  if (!me) return null;
  const tabs = visibleTabs(section("settings"), { me, facts: inst.facts }).filter((t) => t.id !== "overview");
  const online = inst.runners.filter((r) => r.online).length;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Settings</h1>
          <p className="sub">Set up once. Nothing here is needed to run a pipeline day to day.</p>
        </div>
      </header>
      <ul className="guided-tiles guided-tiles-wide">
        {tabs.map((t) => (
          <li key={t.id}>
            <Link href={t.href} className="guided-tile guided-tile-block">
              <strong>{t.label}</strong>
              <span className="faint">{BLURB[t.id]}</span>
              {t.id === "runners" && inst.loaded && <span className="faint">{inst.runners.length === 0 ? "None yet" : `${online} of ${inst.runners.length} online`}</span>}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** People and access: how people and programs prove who they are. Each choice that is on is a card. */
export function AccessPage() {
  const me = useMe();
  if (!me) return null;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>People and access</h1>
          <p className="sub">Signed in as <strong>{me.user}</strong>{me.groups.length > 0 ? `, in ${me.groups.join(", ")}` : ""}. {me.auth_mode === "none" ? "Sign-in is off, so everyone acts as this user." : "Groups decide what each person may do."}</p>
        </div>
      </header>
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
