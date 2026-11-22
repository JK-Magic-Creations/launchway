#!/usr/bin/env python3
"""TEST ONLY. A stand-in for a GitOps controller's sync, for the cells of the upgrade matrix that
need one when no real controller is installed. It does what Argo CD does with a Helm source:

  1. `helm template` with no cluster view (no `lookup`; Helm's own install/upgrade flags are not set);
  2. the resources annotated helm.sh/hook pre-install/pre-upgrade run first (PreSync), in hook-weight
     order; a before-hook-creation policy deletes the old one first; a Job is waited for; a
     hook-succeeded policy deletes it afterwards;
  3. every other resource is applied (kubectl apply --server-side), then
  4. resources the previous sync applied and this one does not render are PRUNED, unless pruning is
     off, or the live object carries Prune=false (argocd.argoproj.io/sync-options).

Usage: gitops-sim.py --chart DIR --values FILE [--values FILE] [--set a=b] --release R --namespace N
                     [--no-prune] [--state FILE]
Exit 0 on a completed sync; 1 when the render fails ("ComparisonError") or a hook or apply fails.
Nothing is applied when the render fails, as with the real controller.
"""
import argparse, json, os, subprocess, sys, time
import yaml


def run(cmd, **kw):
    return subprocess.run(cmd, text=True, capture_output=True, **kw)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--chart", required=True)
    ap.add_argument("--values", action="append", default=[])
    ap.add_argument("--set", action="append", default=[])
    ap.add_argument("--release", required=True)
    ap.add_argument("--namespace", required=True)
    ap.add_argument("--no-prune", action="store_true")
    ap.add_argument("--state", required=True)
    a = ap.parse_args()

    cmd = ["helm", "template", a.release, a.chart, "--namespace", a.namespace]
    for v in a.values:
        cmd += ["-f", v]
    for s in a.set:
        cmd += ["--set", s]
    r = run(cmd)
    if r.returncode != 0:
        print("ComparisonError: " + r.stderr.strip().splitlines()[-1] if r.stderr.strip() else "ComparisonError", file=sys.stderr)
        print(r.stderr, file=sys.stderr)
        return 1
    docs = [d for d in yaml.safe_load_all(r.stdout) if d]
    ann = lambda d: (d.get("metadata", {}).get("annotations") or {})
    hooks, rest = [], []
    for d in docs:
        h = ann(d).get("helm.sh/hook", "")
        if "pre-install" in h or "pre-upgrade" in h:
            hooks.append(d)
        elif h:
            continue  # post hooks and test hooks: not run here
        else:
            rest.append(d)
    hooks.sort(key=lambda d: int(ann(d).get("helm.sh/hook-weight", "0")))

    def kubectl(*args, inp=None):
        return run(["kubectl", *args], input=inp)

    def ident(d):
        return "%s/%s/%s" % (d["kind"], d["metadata"].get("namespace", a.namespace), d["metadata"]["name"])

    # PreSync hooks.
    for d in hooks:
        pol = ann(d).get("helm.sh/hook-delete-policy", "before-hook-creation")
        ns = d["metadata"].get("namespace", a.namespace)
        if "before-hook-creation" in pol:
            kubectl("-n", ns, "delete", d["kind"].lower(), d["metadata"]["name"], "--ignore-not-found", "--wait=true")
        # A hook is not part of the release: strip the hook annotations' effect by applying as is.
        r = kubectl("apply", "-f", "-", inp=yaml.safe_dump(d))
        if r.returncode != 0:
            print("PreSync apply failed: " + r.stderr, file=sys.stderr)
            return 1
        if d["kind"] == "Job":
            w = kubectl("-n", ns, "wait", "--for=condition=complete", "job/" + d["metadata"]["name"], "--timeout=180s")
            if w.returncode != 0:
                print("PreSync hook Job %s did not complete:\n%s" % (d["metadata"]["name"], kubectl("-n", ns, "logs", "job/" + d["metadata"]["name"], "--tail=20").stdout), file=sys.stderr)
                return 1
            print("PreSync hook Job %s completed" % d["metadata"]["name"])
        if "hook-succeeded" in pol:
            kubectl("-n", ns, "delete", d["kind"].lower(), d["metadata"]["name"], "--ignore-not-found", "--wait=false")

    # Sync: apply everything else, in the order rendered (namespaces first).
    rest.sort(key=lambda d: 0 if d["kind"] == "Namespace" else 1)
    for d in rest:
        r = kubectl("apply", "--server-side", "--force-conflicts", "--field-manager=gitops-sim", "-f", "-", inp=yaml.safe_dump(d))
        if r.returncode != 0:
            print("Sync failed for %s: %s" % (ident(d), r.stderr.strip()), file=sys.stderr)
            return 1
    now = sorted(ident(d) for d in rest)
    old = []
    if os.path.exists(a.state):
        old = json.load(open(a.state))
    gone = [i for i in old if i not in now]
    pruned, kept = [], []
    for i in gone:
        kind, ns, name = i.split("/")
        if a.no_prune:
            kept.append(i)
            continue
        live = kubectl("-n", ns, "get", kind.lower(), name, "-o", "json")
        if live.returncode != 0:
            continue
        an = (json.loads(live.stdout)["metadata"].get("annotations") or {})
        if "Prune=false" in an.get("argocd.argoproj.io/sync-options", ""):
            kept.append(i)
            continue
        kubectl("-n", ns, "delete", kind.lower(), name, "--ignore-not-found", "--wait=false")
        pruned.append(i)
    json.dump(now, open(a.state, "w"))
    print("synced %d resources; pruned %s; left in place %s" % (len(rest), pruned, kept))
    return 0


sys.exit(main())
