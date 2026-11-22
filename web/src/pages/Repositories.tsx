import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { forgeApi, type HostedRepo, type SshKey } from "../forgeApi";
import { RepoGeneral } from "./RepoGeneral";
import { RepoReach } from "./RepoReach";
import { HooksPanel } from "./HooksPanel";
import { MergeRulesPanel } from "./MergeRules";
import { ProtectedTags } from "./ProtectedTags";
import { PageTabs, useTab } from "../components/PageTabs";
import { useHosting } from "./SshKeys";
import "../styles/forge.css";
import { prose } from "../prose";
import { BtnPrimary, DivLoading, DivPage, LabelField, PSub, SpanFaint, SpanHidden } from "../kit/bits";
const P = await prose("pages.Repositories");

function Off() {
  return (
    <DivPage>
      <PageHead eyebrow="Code" title="Repositories" />
      <Empty title={P.t0}>{P.t1}<Mono>forge.hosting</Mono>{P.t2}</Empty>
    </DivPage>
  );
}

// The grouped, searchable list loads after the first paint; the flat list is what shows meanwhile.
const RepositoriesExplorer = lazy(() => import(P.t3));

/** The repositories Launchway hosts, which the person may read. */
export function RepositoriesPage() {
  const hosting = useHosting();
  const [repos, setRepos] = useState<HostedRepo[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [opened] = useState(Date.now);
  useEffect(() => { if (hosting === "on") forgeApi.repositories().then(setRepos).catch(setError); }, [hosting]);
  if (hosting === null) return null;
  if (hosting === "off") return <Off />;
  const owners = [...new Set((repos ?? []).map((r) => r.home).filter((o): o is string => !!o))];
  const flat = repos && (
        <Sheet title="Hosted repositories">
      <ul className="rowlist">
        {repos.map((r) => (
          <li key={r.id}>
            <div className="main"><div className="nm"><Link href={`/repositories/${encodeURIComponent(r.home ?? "")}/${encodeURIComponent(r.name)}`}>{r.path}</Link> <Chip>{r.visibility}</Chip>{r.archived && <Chip tone="warn">archived</Chip>}</div>
              <div className="sb faint">default branch {r.default_branch}</div></div>
          </li>
        ))}
      </ul>
    </Sheet>
  );
  return (
    <DivPage>
      <PageHead eyebrow="Code" title="Repositories"
        sentence={repos && repos.length === 0 ? P.t4 : `${repos?.length ?? 0} hosted by Launchway.`}
        actions={<><Link href="/graph" className="btn btn-sm">Graph</Link> <Link href="/settings/access/ssh-keys" className="btn btn-sm">SSH keys</Link></>} />
      <ErrorBox error={error} />
      {repos && repos.length > 0 && (
        <Suspense fallback={flat}><RepositoriesExplorer repos={repos} now={opened} /></Suspense>
      )}
      {owners.length > 0 && (
        <Sheet title="By owner">
          <ul className="rowlist">{owners.map((o) => <li key={o}><div className="main"><div className="nm"><Link href={`/owners/${encodeURIComponent(o)}`}>{o}</Link></div><div className="sb faint">{P.t5}</div></div></li>)}</ul>
        </Sheet>
      )}
    </DivPage>
  );
}

/** The address to clone from, with a switch between HTTPS and SSH. SSH is offered only when this server serves it. */
export function CloneBox({ repo }: { repo: HostedRepo }) {
  const ssh = repo.clone.ssh;
  const [how, setHow] = useState<"https" | "ssh">("https");
  const addr = how === "ssh" && ssh ? ssh : repo.clone.https;
  const [copied, setCopied] = useState(false);
  const copy = async () => { try { await navigator.clipboard.writeText(`git clone ${addr}`); setCopied(true); } catch { setCopied(false); } };
  return (
    <Sheet title="Clone" pad>
      {ssh && (
        <div className="seg" role="tablist" aria-label="Clone with">
          <button type="button" role="tab" aria-selected={how === "https"} className={how === "https" ? "btn btn-sm btn-primary" : "btn btn-sm"} onClick={() => { setHow("https"); setCopied(false); }}>HTTPS</button>{" "}
          <button type="button" role="tab" aria-selected={how === "ssh"} className={how === "ssh" ? "btn btn-sm btn-primary" : "btn btn-sm"} onClick={() => { setHow("ssh"); setCopied(false); }}>SSH</button>
        </div>
      )}
      {!addr
        ? <PSub role="status">{P.t6}<Mono>forge.public_url</Mono> {P.t7}</PSub>
        : <div className="tokenvalue">
          <input readOnly value={addr} aria-label="Clone address" onFocus={(e) => e.currentTarget.select()} className="mono" />
          <button type="button" className="btn" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
        </div>}
      {addr && how === "https"
        ? <PSub>Sign in with an <Link href="/settings/access/keys">API token</Link> as the password.</PSub>
        : addr && <PSub>Add your public key under <Link href="/settings/access/ssh-keys">SSH keys</Link>; <Link href={P.t8}>{P.t9}</Link> the first time.</PSub>}
    </Sheet>
  );
}

function DeployKeys({ repo }: { repo: HostedRepo }) {
  const [keys, setKeys] = useState<SshKey[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const [write, setWrite] = useState(false);
  const owner = repo.home ?? "";
  const load = useCallback(() => { forgeApi.deployKeys(owner, repo.name).then((k) => { setKeys(k); setError(null); }).catch(setError); }, [owner, repo.name]);
  useEffect(load, [load]);
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try { await forgeApi.addDeployKey(owner, repo.name, { name: name.trim(), public_key: pub.trim(), write }); setName(""); setPub(""); setWrite(false); load(); } catch (err) { setError(err); }
  };
  return (
    <>
      <Sheet title="Add a deploy key" pad>
        <PSub>{P.t10}</PSub>
        <form onSubmit={add} className="inputs-form" aria-label="Add a deploy key">
          <LabelField><span>Name</span><input value={name} maxLength={100} placeholder="for example: build server" onChange={(e) => setName(e.target.value)} /></LabelField>
          <LabelField><span>Public key</span><textarea value={pub} rows={3} required className="mono" placeholder="ssh-ed25519 AAAA…" onChange={(e) => setPub(e.target.value)} /></LabelField>
          <label className="check"><input type="checkbox" checked={write} onChange={(e) => setWrite(e.target.checked)} /> Allow this key to push</label>
          <div><BtnPrimary disabled={!pub.trim()}>Add deploy key</BtnPrimary></div>
        </form>
      </Sheet>
      <ErrorBox error={error} />
      <Sheet title="Deploy keys">
        {keys && keys.length === 0 && <Empty title="No deploy keys" />}
        {keys && keys.length > 0 && (
          <table className="table">
            <thead><tr><th>Name</th><th>Fingerprint</th><th>Access</th><th>Last used</th><th><SpanHidden>Actions</SpanHidden></th></tr></thead>
            <tbody>{keys.map((k) => (
              <tr key={k.id}><td>{k.name}</td><td className="mono small">{k.fingerprint}</td><td>{k.can_write ? <Chip tone="warn">read and write</Chip> : <Chip>read only</Chip>}</td>
                <td>{k.last_used_at ? `${clock(Date.parse(k.last_used_at))} from ${k.last_used_from}` : "never"}</td>
                <td className="td-actions"><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => { if (window.confirm(`Delete the deploy key "${k.name}"?`)) forgeApi.deleteDeployKey(owner, repo.name, k.id).then(load, setError); }}>Delete</button></td></tr>
            ))}</tbody>
          </table>
        )}
      </Sheet>
    </>
  );
}

function Protected({ repo }: { repo: HostedRepo }) {
  const [text, setText] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const owner = repo.home ?? "";
  useEffect(() => { forgeApi.protectedBranches(owner, repo.name).then((p) => setText(p.join(", "))).catch(setError); }, [owner, repo.name]);
  if (text === null) return <ErrorBox error={error} />;
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null); setSaved(false);
    try { const p = await forgeApi.setProtectedBranches(owner, repo.name, text.split(",").map((s) => s.trim()).filter(Boolean)); setText(p.join(", ")); setSaved(true); } catch (err) { setError(err); }
  };
  return (
    <Sheet title="Protected branches" pad>
      <PSub>{P.t11}<Mono>main, release/*</Mono>.</PSub>
      <form onSubmit={save} className="inputs-form" aria-label="Protected branches">
        <LabelField><span>Branches</span><input value={text} placeholder="main, release/*" onChange={(e) => { setText(e.target.value); setSaved(false); }} /></LabelField>
        <ErrorBox error={error} />
        <div><BtnPrimary>Save</BtnPrimary>{saved && <SpanFaint> Saved.</SpanFaint>}</div>
      </form>
    </Sheet>
  );
}

/** The clone address and the settings of one hosted repository, for its page (pages/Repository.tsx). The deploy keys,
 * webhooks and protected branches are for the repository's administrators: their tabs appear only when the server
 * lets this person see them. */
export function RepositorySettings({ owner, name }: { owner: string; name: string }) {
  const [repo, setRepo] = useState<HostedRepo | null>(null);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [tab, setTab] = useTab(["keys", "reach", "hooks", "branches", "merge"], "keys");
  useEffect(() => {
    forgeApi.repository(owner, name).then(setRepo).catch(setError);
    forgeApi.deployKeys(owner, name).then(() => setAdmin(true), () => setAdmin(false));
  }, [owner, name]);
  if (error) return <ErrorBox error={error} />;
  if (!repo) return <DivLoading role="status">{P.t12}</DivLoading>;
  return (
    <>
      <CloneBox repo={repo} />
      <RepoGeneral repo={repo} admin={admin} onChanged={setRepo} />
      {admin && (
        <>
          <PageTabs current={tab} onChange={setTab}
            tabs={[{ id: "keys", label: "Deploy keys" }, { id: "reach", label: "Who can reach it" }, { id: "hooks", label: "Webhooks" }, { id: "branches", label: "Branches and tags" }, { id: "merge", label: "Merge rules" }]} />
          {tab === "keys" && <DeployKeys repo={repo} />}
          {tab === "reach" && <RepoReach repo={repo} />}
          {tab === "hooks" && <HooksPanel scope={{ owner: repo.home ?? "", name: repo.name }} who={`events of ${repo.path}`} />}
          {tab === "branches" && <><Protected repo={repo} /><ProtectedTags repo={repo} /></>}
          {tab === "merge" && <MergeRulesPanel path={repo.path} />}
        </>
      )}
    </>
  );
}

/** An owner's settings: the webhooks that cover every repository the owner has. */
export function OwnerPage({ owner }: { owner: string }) {
  const hosting = useHosting();
  if (hosting === null) return null;
  if (hosting === "off") return <Off />;
  return (
    <DivPage>
      <PageHead eyebrow={<Link href="/repositories">Repositories</Link>} title={owner} sentence={P.t13} />
      <HooksPanel scope={{ owner }} who={`events of every repository of ${owner}`} />
    </DivPage>
  );
}
