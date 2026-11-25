import type { ReactNode } from "react";
import { useMe } from "../auth";
import { Link } from "../components/ui";
import { PageHead } from "../kit/layout";
import { Icon } from "../kit/Icon";
import type { IconName } from "../kit/iconPaths";
import { useHosting } from "./SshKeys";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivPage } from "../kit/bits";
import "../styles/people-access.css";
const P = await prose("pages.Settings");

/** One choice on the page: an icon, a name, one plain sentence, and an arrow. The whole card is the link. */
function AccessTile({ href, icon, name, children }: { href: string; icon: IconName; name: string; children: ReactNode }) {
  return (
    <li>
      <Link href={href} className="guided-tile access-tile">
        <span className="access-tile-icon" aria-hidden="true"><Icon name={icon} /></span>
        <span className="access-tile-text">
          <span className="access-tile-name">{name}</span>
          <span className="access-tile-desc">{children}</span>
        </span>
        <span className="access-tile-go" aria-hidden="true"><Icon name="chev" size="s" /></span>
      </Link>
    </li>
  );
}

/** A titled group of choices; drawn only when it holds at least one. */
function AccessGroup({ id, title, children }: { id: string; title: string; children: ReactNode[] }) {
  if (!children.some(Boolean)) return null;
  return (
    <section className="access-group" aria-labelledby={id}>
      <h2 className="h-sm" id={id}>{title}</h2>
      <ul className="guided-tiles guided-tiles-wide">{children}</ul>
    </section>
  );
}

/** People and access: how people and programs prove who they are. Each choice that is on is a card. */
export function AccessPage() {
  const me = useMe();
  const hosting = useHosting();
  if (!me) return null;
  const how = me.auth_mode === "oidc" ? P.t1 : me.auth_mode === "local" ? P.t2 : P.t0;
  return (
    <DivPage>
      <PageHead eyebrow="Settings" title="People and access" sentence={P.t3} />
      <dl className="card glance-grid access-who">
        <div><dt>{P.t4}</dt><dd><strong>{me.user}</strong></dd></div>
        <div><dt>{P.t5}</dt><dd>{me.groups.length > 0
          ? <ul className="access-groups">{me.groups.map((g) => <li key={g} className="pill">{g}</li>)}</ul>
          : <span className="faint">{P.t6}</span>}</dd></div>
        <div><dt>{P.t7}</dt><dd>{how}</dd></div>
      </dl>
      <AccessGroup id="access-keys" title={P.t8}>{[
        hosting === "on" && <AccessTile key="ssh" href="/settings/access/ssh-keys" icon="terminal" name="SSH keys">{P.t9}</AccessTile>,
        hosting === "on" && <AccessTile key="sign" href="/settings/access/signing-keys" icon="shield" name="Signing keys">{P.t10}</AccessTile>,
        me.auth_mode === "oidc" && <AccessTile key="tokens" href="/settings/access/keys" icon="key" name="Keys for scripts">{P.t11}</AccessTile>,
      ]}</AccessGroup>
      <AccessGroup id="access-more" title={P.t12}>{[
        <AccessTile key="notify" href="/settings/notifications" icon="bell" name="Notifications">{P.t13}</AccessTile>,
        me.mcp?.enabled !== false && <AccessTile key="agent" href="/settings/access/agents" icon="spark" name="Connect an agent">{terms().say(P.t14)}</AccessTile>,
      ]}</AccessGroup>
      <AccessGroup id="access-server" title={P.t15}>{[
        me.admin && me.auth_mode === "none" && <AccessTile key="signin" href={P.t16} icon="lock" name="Turn on sign-in">{P.t17}</AccessTile>,
      ]}</AccessGroup>
    </DivPage>
  );
}
