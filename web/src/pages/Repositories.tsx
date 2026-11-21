import { useCallback, useEffect, useState } from "react";
import { clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { forgeApi, type HostedRepo, type SshKey } from "../forgeApi";
import { HooksPanel } from "./HooksPanel";
import { useHosting } from "./SshKeys";

function Off() {
  return (
    <div className="page">
      <PageHead eyebrow="Code" title="Repositories" />
      <Empty title="Git hosting is off on this server">Launchway can host Git repositories itself. An administrator turns it on with the installation setting <Mono>forge.hosting</Mono>; nothing is deleted when it is off.</Empty>
    </div>
  );
}

/** The repositories Launchway hosts, which the person may read. */
export function RepositoriesPage() {
  const hosting = useHosting();
  const [repos, setRepos] = useState<HostedRepo[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { if (hosting === "on") forgeApi.repositories().then(setRepos).catch(setError); }, [hosting]);
  if (hosting === null) return null;
  if (hosting === "off") return <Off />;
  const owners = [...new Set((repos ?? []).map((r) => r.home).filter((o): o is string => !!o))];
  return (
    <div className="page">
      <PageHead eyebrow="Code" title="Repositories"
        sentence={repos && repos.length === 0 ? "No repository is hosted here yet." : `${repos?.length ?? 0} hosted by Launchway.`}
        actions={<Link href="/settings/access/ssh-keys" className="btn btn-sm">SSH keys</Link>} />
      <ErrorBox error={error} />
      {repos && repos.length > 0 && (
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
      )}
      {owners.length > 0 && (
        <Sheet title="By owner">
          <ul className="rowlist">{owners.map((o) => <li key={o}><div className="main"><div className="nm"><Link href={`/owners/${encodeURIComponent(o)}`}>{o}</Link></div><div className="sb faint">Webhooks for every repository of this owner</div></div></li>)}</ul>
        </Sheet>
      )}
    </div>
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
      <div className="tokenvalue">
        <input readOnly value={addr ?? ""} aria-label="Clone address" onFocus={(e) => e.currentTarget.select()} className="mono" />
        <button type="button" className="btn" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
      </div>
      {how === "https"
        ? <p className="sub">Sign in with an <Link href="/settings/access/keys">API token</Link> as the password.</p>
        : <p className="sub">Add your public key under <Link href="/settings/access/ssh-keys">SSH keys</Link>; <Link href="/settings/access/ssh-keys#host-keys">check the server's fingerprint</Link> the first time.</p>}
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
        <p className="sub">A deploy key opens this one repository over SSH, for a machine such as a build server. It can only read unless you let it write.</p>
        <form onSubmit={add} className="inputs-form" aria-label="Add a deploy key">
          <label className="field"><span>Name</span><input value={name} maxLength={100} placeholder="for example: build server" onChange={(e) => setName(e.target.value)} /></label>
          <label className="field"><span>Public key</span><textarea value={pub} rows={3} required className="mono" placeholder="ssh-ed25519 AAAA…" onChange={(e) => setPub(e.target.value)} /></label>
          <label className="check"><input type="checkbox" checked={write} onChange={(e) => setWrite(e.target.checked)} /> Allow this key to push</label>
          <div><button type="submit" className="btn btn-primary" disabled={!pub.trim()}>Add deploy key</button></div>
        </form>
      </Sheet>
      <ErrorBox error={error} />
      <Sheet title="Deploy keys">
        {keys && keys.length === 0 && <Empty title="No deploy keys" />}
        {keys && keys.length > 0 && (
          <table className="table">
            <thead><tr><th>Name</th><th>Fingerprint</th><th>Access</th><th>Last used</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
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
      <p className="sub">A protected branch takes a direct push only from an administrator of this repository, and cannot be deleted. Names or patterns, separated by commas: <Mono>main, release/*</Mono>.</p>
      <form onSubmit={save} className="inputs-form" aria-label="Protected branches">
        <label className="field"><span>Branches</span><input value={text} placeholder="main, release/*" onChange={(e) => { setText(e.target.value); setSaved(false); }} /></label>
        <ErrorBox error={error} />
        <div><button type="submit" className="btn btn-primary">Save</button>{saved && <span className="faint"> Saved.</span>}</div>
      </form>
    </Sheet>
  );
}

type Tab = "clone" | "keys" | "hooks" | "branches";

/** One hosted repository: where to clone it from, and its settings. */
export function RepositoryPage({ owner, name }: { owner: string; name: string }) {
  const [repo, setRepo] = useState<HostedRepo | null>(null);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [tab, setTab] = useState<Tab>("clone");
  useEffect(() => {
    forgeApi.repository(owner, name).then(setRepo).catch(setError);
    // The settings are for the repository's administrators: the tabs appear only when the server lets this person see them.
    forgeApi.deployKeys(owner, name).then(() => setAdmin(true), () => setAdmin(false));
  }, [owner, name]);
  if (error) return <div className="page"><PageHead eyebrow="Code" title={`${owner}/${name}`} /><ErrorBox error={error} /></div>;
  if (!repo) return null;
  return (
    <div className="page">
      <PageHead eyebrow={<Link href="/repositories">Repositories</Link>} title={repo.path}
        badge={<> <Chip>{repo.visibility}</Chip>{repo.archived && <Chip tone="warn">archived</Chip>}</>}
        sentence={`Default branch ${repo.default_branch}.`} />
      <div className="tabs" role="tablist" aria-label="Repository">
        {([["clone", "Clone"], ["keys", "Deploy keys"], ["hooks", "Webhooks"], ["branches", "Protected branches"]] as [Tab, string][]).filter(([t]) => t === "clone" || admin).map(([t, label]) => (
          <button key={t} type="button" role="tab" aria-selected={tab === t} className={tab === t ? "tab on" : "tab"} onClick={() => setTab(t)}>{label}</button>
        ))}
      </div>
      {tab === "clone" && <CloneBox repo={repo} />}
      {tab === "keys" && <DeployKeys repo={repo} />}
      {tab === "hooks" && <HooksPanel scope={{ owner: repo.home ?? "", name: repo.name }} who={`events of ${repo.path}`} />}
      {tab === "branches" && <Protected repo={repo} />}
    </div>
  );
}

/** An owner's settings: the webhooks that cover every repository the owner has. */
export function OwnerPage({ owner }: { owner: string }) {
  const hosting = useHosting();
  if (hosting === null) return null;
  if (hosting === "off") return <Off />;
  return (
    <div className="page">
      <PageHead eyebrow={<Link href="/repositories">Repositories</Link>} title={owner} sentence="Settings for every repository this owner has. Webhooks here receive the events of all of them." />
      <HooksPanel scope={{ owner }} who={`events of every repository of ${owner}`} />
    </div>
  );
}
