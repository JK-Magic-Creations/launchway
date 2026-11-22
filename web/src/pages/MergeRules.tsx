import { useEffect, useState } from "react";
import { ErrorBox, Mono } from "../components/ui";
import { Sheet } from "../kit/layout";
import { pullsApi, type Action, type MergeRule, type PullSettings } from "../pullsApi";

const blank = (pattern = ""): MergeRule => ({ pattern, required_approvals: 1, approval_role: "operator", required_checks: [], require_up_to_date: false,
  require_resolved_threads: false, no_self_approval: true, require_signed_commits: false, allow_admin_bypass: false });
const METHODS: [keyof PullSettings, string, string][] = [["allow_merge", "merge", "Merge commit"], ["allow_squash", "squash", "Squash and merge"], ["allow_rebase", "rebase", "Rebase and merge"]];

/** Merge rules and pull request settings of one hosted repository. What the server's `actions` list as unavailable is
 * not offered: a person who may not edit sees the values and the reason, and the two rules about verified commits
 * are switches only where the server has them on. */
export function MergeRulesPanel({ path }: { path: string }) {
  const [rules, setRules] = useState<MergeRule[] | null>(null);
  const [actions, setActions] = useState<Action[]>([]);
  const [settings, setSettings] = useState<PullSettings | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState("");
  const [newBranch, setNewBranch] = useState("");
  const load = () => {
    pullsApi.rules(path).then((r) => { setRules(r.rules); setActions(r.actions); }).catch(setError);
    pullsApi.settings(path).then(setSettings).catch(setError);
  };
  useEffect(load, [path]);
  const action = (n: string) => actions.find((a) => a.name === n);
  if (!rules || !settings) return <ErrorBox error={error} />;
  const editRules = action("edit_rules")?.available === true;
  const editSettings = action("edit_settings")?.available === true;
  const signed = action("require_signed_commits");
  const verifiedPush = action("require_verified_push");
  const patch = (i: number, p: Partial<MergeRule>) => { setRules(rules.map((r, j) => (j === i ? { ...r, ...p } : r))); setSaved(""); };
  const saveRule = async (i: number) => {
    setError(null); setSaved("");
    try { const r = await pullsApi.setRules(path, [rules[i]]); setRules(r.rules); setActions(r.actions); setSaved(rules[i].pattern); } catch (e) { setError(e); }
  };
  const saveSettings = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null); setSaved("");
    try { setSettings(await pullsApi.setSettings(path, settings)); setSaved("settings"); } catch (err) { setError(err); }
  };
  const allowed = METHODS.filter(([k]) => settings[k]);
  return (
    <>
      <Sheet title="Merge rules" pad>
        <p className="sub">A rule covers a branch or a pattern such as <Mono>release/*</Mono> and also protects it from direct pushes by anyone but an administrator. Pull requests into it cannot be merged until the rule is met.</p>
        {!editRules && <p className="sub" role="note">You can read these but not change them: {action("edit_rules")?.reason ?? "you need the admin role"}.</p>}
        {rules.length === 0 && <p className="faint">No rules yet. Add one for the branch you want reviewed.</p>}
        {rules.map((r, i) => (
          <form key={r.pattern} className="inputs-form card" aria-label={`Merge rule for ${r.pattern}`} onSubmit={(e) => { e.preventDefault(); void saveRule(i); }}>
            <h3 className="h-sm"><Mono>{r.pattern}</Mono></h3>
            <fieldset disabled={!editRules} className="inputs-form">
              <label className="field"><span>Approvals needed</span>
                <input type="number" min={0} max={20} value={r.required_approvals} onChange={(e) => patch(i, { required_approvals: Number(e.target.value) })} /></label>
              <label className="field"><span>Approvals count from</span>
                <select value={r.approval_role} onChange={(e) => patch(i, { approval_role: e.target.value })}>
                  <option value="operator">Anyone who can push to the repository</option>
                  <option value="admin">Administrators of the repository only</option>
                </select></label>
              <label className="field"><span>Checks that must have passed</span>
                <input value={r.required_checks.join(", ")} placeholder="build, build/test" onChange={(e) => patch(i, { required_checks: e.target.value.split(",").map((c) => c.trim()).filter(Boolean) })} />
                <small>A pipeline name, or a step as <Mono>pipeline/step</Mono>. A bare name matches a step of that name in any pipeline.</small></label>
              <label className="check"><input type="checkbox" checked={r.require_up_to_date} onChange={(e) => patch(i, { require_up_to_date: e.target.checked })} /> The branch must contain the latest target</label>
              <label className="check"><input type="checkbox" checked={r.require_resolved_threads} onChange={(e) => patch(i, { require_resolved_threads: e.target.checked })} /> Every review thread must be resolved</label>
              <label className="check"><input type="checkbox" checked={r.no_self_approval} onChange={(e) => patch(i, { no_self_approval: e.target.checked })} /> The author's own approval does not count</label>
              <label className="check"><input type="checkbox" checked={!!r.require_owner_approval} onChange={(e) => patch(i, { require_owner_approval: e.target.checked })} /> An owner of every changed file must approve (from <Mono>.launchway/owners</Mono>)</label>
              <label className="check"><input type="checkbox" checked={!!r.require_latest_push_approved_by_other} onChange={(e) => patch(i, { require_latest_push_approved_by_other: e.target.checked })} /> The latest push must be approved by someone other than who pushed it</label>
              <label className="check"><input type="checkbox" checked={!!r.dismiss_stale_approvals} onChange={(e) => patch(i, { dismiss_stale_approvals: e.target.checked })} /> Dismiss approvals when new commits are pushed to a pull request into it</label>
              <label className="check"><input type="checkbox" checked={!!r.merge_queue} onChange={(e) => patch(i, { merge_queue: e.target.checked })} /> Merge through the merge queue (tested on the latest target, one at a time)</label>
              <label className="field"><span>Checks required only when matching files change</span>
                <input value={(r.required_checks_by_path ?? []).map((c) => `${c.paths.join(",")}=${c.check}`).join("; ")} placeholder="docs/,*.md=docs; /cmd/**=build"
                  onChange={(e) => patch(i, { required_checks_by_path: e.target.value.split(";").map((part) => part.trim()).filter(Boolean).map((part) => { const [paths, check] = part.split("="); return { paths: (paths ?? "").split(",").map((x) => x.trim()).filter(Boolean), check: (check ?? "").trim() }; }) })} />
                <small>Each entry is <Mono>paths=check</Mono>. A pipeline that its own path filter kept from running never blocks.</small></label>
              <label className="check"><input type="checkbox" checked={r.allow_admin_bypass} onChange={(e) => patch(i, { allow_admin_bypass: e.target.checked })} /> Administrators may merge past the rule, saying why (recorded)</label>
              {signed?.available && <label className="check"><input type="checkbox" checked={r.require_signed_commits} onChange={(e) => patch(i, { require_signed_commits: e.target.checked })} /> Every commit a merge brings must be verified (signed by a registered key); Launchway signs the merge commit itself</label>}
              {verifiedPush?.available && <label className="check"><input type="checkbox" checked={!!r.require_verified_push} onChange={(e) => patch(i, { require_verified_push: e.target.checked })} /> Only verified commits may be pushed to it (over HTTPS or SSH, administrators too)</label>}
            </fieldset>
            {signed && !signed.available && <p className="faint small">Verified commits cannot be required here. {signed.reason}</p>}
            {editRules && <div><button type="submit" className="btn btn-primary btn-sm">Save the rule for {r.pattern}</button>{saved === r.pattern && <span className="faint"> Saved.</span>}</div>}
          </form>
        ))}
        {editRules && (
          <form className="inputs-form" aria-label="Add a merge rule" onSubmit={(e) => { e.preventDefault(); const p = newBranch.trim(); if (p) { setRules([...rules, blank(p)]); setNewBranch(""); } }}>
            <label className="field"><span>Add a rule for a branch</span><input value={newBranch} placeholder="main" onChange={(e) => setNewBranch(e.target.value)} /></label>
            <div><button type="submit" className="btn btn-sm" disabled={!newBranch.trim()}>Add the rule</button><span className="faint small"> It takes effect when you save it.</span></div>
          </form>
        )}
      </Sheet>
      <Sheet title="Pull request settings" pad>
        <form onSubmit={saveSettings} className="inputs-form" aria-label="Pull request settings">
          {!editSettings && <p className="sub" role="note">You can read these but not change them: {action("edit_settings")?.reason ?? "you need the admin role"}.</p>}
          <fieldset disabled={!editSettings} className="inputs-form">
            <fieldset className="events"><legend>Ways to merge</legend>
              {METHODS.map(([k, , label]) => (
                <label key={k} className="check"><input type="checkbox" checked={settings[k] as boolean} onChange={(e) => { setSettings({ ...settings, [k]: e.target.checked }); setSaved(""); }} /> {label}</label>
              ))}
            </fieldset>
            <label className="field"><span>Offered first</span>
              <select value={settings.default_method} onChange={(e) => { setSettings({ ...settings, default_method: e.target.value }); setSaved(""); }}>
                {allowed.map(([, m, label]) => <option key={m} value={m}>{label}</option>)}
              </select></label>
            <label className="check"><input type="checkbox" checked={settings.delete_branch_after_merge} onChange={(e) => { setSettings({ ...settings, delete_branch_after_merge: e.target.checked }); setSaved(""); }} /> Delete the source branch after a merge</label>
            <label className="field"><span>Ask reviewers automatically</span>
              <select value={settings.assign_reviewers ?? ""} onChange={(e) => { setSettings({ ...settings, assign_reviewers: e.target.value }); setSaved(""); }}>
                <option value="">Not at all</option>
                <option value="owners">Every owner of the changed files</option>
                <option value="round_robin">One owner per file, by turns</option>
                <option value="least_loaded">One owner per file, whoever has the fewest open requests</option>
              </select><small>From <Mono>.launchway/owners</Mono> on the target branch, when a pull request is ready for review.</small></label>
            <label className="check"><input type="checkbox" checked={settings.dismiss_stale_approvals} onChange={(e) => { setSettings({ ...settings, dismiss_stale_approvals: e.target.checked }); setSaved(""); }} /> Dismiss approvals when new commits are pushed</label>
          </fieldset>
          <ErrorBox error={error} />
          {editSettings && <div><button type="submit" className="btn btn-primary" disabled={allowed.length === 0}>Save the settings</button>{saved === "settings" && <span className="faint"> Saved.</span>}</div>}
        </form>
      </Sheet>
    </>
  );
}
