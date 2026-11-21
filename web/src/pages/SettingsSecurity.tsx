import { useEffect, useState } from "react";
import { useMe } from "../auth";
import { Empty, ErrorBox, Mono } from "../components/ui";
import { encryptionApi, type EncryptionReport } from "../encryptionApi";
import { Chip } from "../kit/Status";
import { Icon } from "../kit/Icon";
import { PageHead, Sheet } from "../kit/layout";
import { terms } from "../terms";

/** Settings → Security: whether what the server stores is encrypted at rest, said plainly. Read-only: the root key is
 * held outside the database and is rotated or replaced on the server host with `launchway encryption`. */
export function SettingsSecurityPage() {
  const me = useMe();
  const [r, setR] = useState<EncryptionReport | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!me?.admin) return;
    let live = true;
    encryptionApi.report().then((x) => live && setR(x), (e) => live && setError(e));
    return () => { live = false; };
  }, [me?.admin]);

  if (!me) return null;
  if (!me.admin) {
    return (
      <div className="page">
        <PageHead eyebrow="Settings" title="Security" />
        <Sheet pad><div className="note"><Icon name="lock" />Only an administrator can see how this server's secrets are protected.</div></Sheet>
      </div>
    );
  }
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="Security"
        sentence="Whether the secrets this server stores are encrypted at rest, and where the key that protects them is held." />
      <ErrorBox error={error} />
      {r === null && !error && <Empty title="Reading the encryption status">One moment.</Empty>}
      {r && (
        <>
          <Sheet title="Encryption at rest" pad>
            <p role={r.encrypted ? "status" : "alert"}>
              <Chip tone={r.encrypted && r.secrets_remaining === 0 ? "ok" : "bad"} icon={r.encrypted ? "lock" : "shield"}>
                {r.encrypted ? (r.secrets_remaining === 0 ? "Encrypted at rest" : "Sealing in progress") : "NOT encrypted at rest"}
              </Chip>{" "}{r.summary}
            </p>
            <dl className="kv">
              <dt>A database dump alone cannot reveal secrets</dt>
              <dd>{r.dump_alone_cannot_reveal_secrets ? "Yes" : "No"}</dd>
              {r.root_key_holder && <><dt>Root key held by</dt><dd><Mono>{r.root_key_holder.kind}</Mono> ({r.root_key_holder.where}){!r.root_key_holder.reachable && <> <Chip tone="bad">not reachable</Chip></>}</dd></>}
              {r.keys && <><dt>Key versions</dt><dd>key-encryption key {r.keys.current_key_encryption_key} is current; {r.keys.data_keys.length} data keys</dd></>}
              <dt>Secrets sealed</dt><dd>{r.secrets_sealed} sealed, {r.secrets_remaining} still in the clear (sealing: {r.migration.state})</dd>
            </dl>
            {r.warnings.map((w) => <div key={w} className="note" role="alert"><Icon name="info" />{w}</div>)}
          </Sheet>
          <Sheet title="What is sealed" pad>
            <ul className="rowlist">
              {r.columns.map((c) => (
                <li key={c.table + c.column}>
                  <div className="main"><div className="nm"><Mono>{c.table}.{c.column}</Mono></div><div className="sb">{c.what}</div></div>
                  <div className="cell"><Chip tone={c.remaining === 0 ? "ok" : "warn"}>{c.sealed} sealed{c.remaining > 0 ? `, ${c.remaining} in the clear` : ""}</Chip></div>
                </li>
              ))}
            </ul>
            <div className="note"><Icon name="info" />Not yet encrypted by Launchway: {r.not_yet_encrypted.join(", ")}{terms().say(". Use volume or database encryption for the database and the {artifact} store until they are.")}</div>
          </Sheet>
          <Sheet title="What to do" pad>
            <ul>{r.advice.map((a) => <li key={a}>{a}</li>)}</ul>
            <p>Rotating or replacing the key is done on the server host: <Mono>launchway encryption rotate</Mono>, <Mono>launchway encryption rewrap</Mono>. <Mono>launchway doctor encryption</Mono> says the same as this page.</p>
          </Sheet>
        </>
      )}
    </div>
  );
}
