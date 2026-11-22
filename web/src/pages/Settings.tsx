import { useMe } from "../auth";
import { Link } from "../components/ui";
import { PageHead } from "../kit/layout";
import { useHosting } from "./SshKeys";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivPage, SpanFaint } from "../kit/bits";
const P = await prose("pages.Settings");

/** People and access: how people and programs prove who they are. Each choice that is on is a card. */
export function AccessPage() {
  const me = useMe();
  const hosting = useHosting();
  if (!me) return null;
  return (
    <DivPage>
      <PageHead eyebrow="Settings" title="People and access"
        sentence={<>Signed in as <strong>{me.user}</strong>{me.groups.length > 0 ? `, in ${me.groups.join(", ")}` : ""}. {me.auth_mode === "none" ? P.t0 : P.t1}</>} />
      <ul className={P.t2}>
        {me.auth_mode === "oidc" && (
          <li><Link href="/settings/access/keys" className="guided-tile guided-tile-block"><strong>Keys for scripts</strong><SpanFaint>{P.t3}</SpanFaint></Link></li>
        )}
        <li><Link href="/settings/notifications" className="guided-tile guided-tile-block"><strong>Notifications</strong><SpanFaint>{P.t4}</SpanFaint></Link></li>
        {hosting === "on" && (
          <li><Link href="/settings/access/ssh-keys" className="guided-tile guided-tile-block"><strong>SSH keys</strong><SpanFaint>{P.t5}</SpanFaint></Link></li>
        )}
        {hosting === "on" && (
          <li><Link href="/settings/access/signing-keys" className="guided-tile guided-tile-block"><strong>Signing keys</strong><SpanFaint>{P.t6}</SpanFaint></Link></li>
        )}
        {me.mcp?.enabled !== false && (
          <li><Link href="/settings/access/agents" className="guided-tile guided-tile-block"><strong>Connect an agent</strong><SpanFaint>{terms().say(P.t7)}</SpanFaint></Link></li>
        )}
        {me.admin && me.auth_mode === "none" && (
          <li><Link href={P.t8} className="guided-tile guided-tile-block"><strong>Turn on sign-in</strong><SpanFaint>{P.t9}</SpanFaint></Link></li>
        )}
      </ul>
    </DivPage>
  );
}
