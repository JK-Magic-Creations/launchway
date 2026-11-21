import { useMe } from "../auth";
import { Chip } from "../kit/Status";
import { useInstallation } from "../guided/facts";
import { section, visibleTabs } from "../nav/model";
import { onLink } from "../router";
import { Icon } from "../kit/Icon";
import { PageHead, Sheet } from "../kit/layout";

const BLURB: Record<string, string> = {
  runners: "The machines that run your steps. Add one, see who is online, or take one away.",
  access: "Keys for scripts, and how to connect an AI assistant.",
  approvals: "Whether the person who starts a run may approve it themselves.",
  rules: "Rules every pipeline is held to before it deploys.",
  security: "Whether the secrets this server stores are encrypted at rest, and where the key is held.",
  audit: "Who changed what, and when. Every sign-in and every change.",
  variables: "Named settings you set once at a level, which flow down to everything below. A lock keeps one from being overridden.",
  config: "Everything an administrator sets up, with a guided set-up for each, kept with its history.",
};

/** Settings: set up once. One row for each place the person is allowed to change things, with its state. */
export function SettingsHomePage() {
  const me = useMe();
  const inst = useInstallation();
  if (!me) return null;
  const tabs = visibleTabs(section("settings"), { me, facts: inst.facts }).filter((t) => t.id !== "overview");
  const online = inst.runners.filter((r) => r.online).length;
  const state = (id: string) => {
    if (id === "runners" && inst.loaded) return inst.runners.length === 0 ? <Chip tone="warn">None yet</Chip> : <Chip tone={online === inst.runners.length ? "ok" : "warn"}>{online} of {inst.runners.length} online</Chip>;
    if (id === "access") return <Chip>{me.auth_mode === "none" ? "sign-in is off" : "sign-in is on"}</Chip>;
    return null;
  };
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="Set up once, then rarely" sentence="Nothing here is needed to run a pipeline day to day. Each row is a place you may change things." />
      <Sheet>
        <ul className="rowlist">
          {me.admin && (
            <li>
              <Icon name="info" />
              <div className="main"><div className="nm"><a href="/setup/guide" onClick={onLink}>Setup guide</a></div><div className="sb">The first-run walk-through: sign-in, the root key, a runner, Git hosting, importing repositories and a first pipeline. Skip any step and come back.</div></div>
              <div className="cell" />
            </li>
          )}
          {tabs.map((t) => (
            <li key={t.id}>
              <Icon name={t.icon ?? "settings"} />
              <div className="main"><div className="nm"><a href={t.href} onClick={onLink}>{t.label}</a></div><div className="sb">{BLURB[t.id]}</div></div>
              <div className="cell">{state(t.id)}</div>
            </li>
          ))}
        </ul>
      </Sheet>
    </div>
  );
}
