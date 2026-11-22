import { useMe } from "../auth";
import { Chip } from "../kit/Status";
import { useInstallation } from "../guided/facts";
import { section, visibleTabs } from "../nav/model";
import { onLink } from "../router";
import { Icon } from "../kit/Icon";
import { PageHead, Sheet } from "../kit/layout";
import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("pages.SettingsHome");

const BLURB: Record<string, string> = {
  runners: P.t0,
  access: P.t1,
  approvals: P.t2,
  rules: P.t3,
  security: P.t4,
  audit: P.t5,
  variables: P.t6,
  config: P.t7,
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
      <PageHead eyebrow="Settings" title="Set up once, then rarely" sentence={P.t8} />
      <Sheet>
        <ul className="rowlist">
          {me.admin && (
            <li>
              <Icon name="info" />
              <div className="main"><div className="nm"><a href="/setup/guide" onClick={onLink}>Setup guide</a></div><div className="sb">{terms().say(P.t9)}</div></div>
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
