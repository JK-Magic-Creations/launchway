import { useState } from "react";
import { ErrorBox, Mono } from "../components/ui";
import { forgeApi, type HostedRepo } from "../forgeApi";
import { Button } from "../kit/layout";
import { navigate } from "../router";

/** Transfer a repository to another owner: it says what moves with it and what the new owner gains, and asks for the
 * repository's address typed in. The old address keeps working until another repository takes it. */
export function RepoTransfer({ repo, owner }: { repo: HostedRepo; owner: string }) {
  const [to, setTo] = useState("");
  const [asking, setAsking] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const target = to.trim();
  const go = async () => {
    setBusy(true); setError(null);
    try {
      const r = await forgeApi.update(owner, repo.name, { owner: target });
      navigate(`/repositories/${r.path}/settings`);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <>
      <h3 className="h-sm">Transfer to another owner</h3>
      <form className="inputs-form" aria-label="Transfer the repository" onSubmit={(e) => { e.preventDefault(); setAsking(true); }}>
        <label className="field"><span>New owner</span><input value={to} onChange={(e) => { setTo(e.target.value); setAsking(false); }} placeholder="owner" /></label>
        <div><Button type="submit" disabled={!target || target === owner}>Transfer…</Button></div>
      </form>
      {asking && (
        <div role="group" aria-label="Confirm the transfer" className="callout callout-warn">
          <p><strong>Move {repo.path} to {target}/{repo.name}.</strong> You need the admin role on <Mono>{target}/{repo.name}</Mono> to do this.</p>
          <p>What moves with it: its history, branches and tags, pull requests, releases, deploy keys, webhooks of this repository, protected branches and tags, merge rules, mirrors, the tokens that name it and the Flights that read it. The old address, <Mono>{repo.path}</Mono>, keeps working for Git over HTTPS and SSH and redirects the API and the web pages, until another repository takes that address.</p>
          <p>What {target} gains: everyone who administers or operates repositories of {target} in the access settings reaches it from now on, and the webhooks you set for all of {target}'s repositories now cover it. People who reached it only because they had access to {owner} lose that access until it is given for {target}: access is configuration and does not move with the repository.</p>
          <label className="field"><span>Type <Mono>{repo.path}</Mono> to confirm</span>
            <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" data-autofocus /></label>
          <div className="actions">
            <Button kind="pri" disabled={busy || typed !== repo.path} onClick={go}>{busy ? "Moving…" : `Transfer to ${target}`}</Button>
            <Button onClick={() => { setAsking(false); setTyped(""); }}>Cancel</Button>
          </div>
        </div>
      )}
      <ErrorBox error={error} />
    </>
  );
}
