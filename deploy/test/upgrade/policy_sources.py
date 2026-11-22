#!/usr/bin/env python3
"""TEST ONLY. Reads rendered chart manifests: fails if a server NetworkPolicy ingress rule has no
source (an ingress rule with ports and no `from` admits every source) or if the step pods' policy
carries an ipBlock by default."""
import sys, yaml
bad = []
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "NetworkPolicy":
        continue
    name = d["metadata"]["name"]
    if name.endswith("-server"):
        for rule in d["spec"].get("ingress", []):
            ports = [p.get("port") for p in rule.get("ports", [])]
            if "from" not in rule and 2222 not in ports:  # the SSH port is an opt-in feature (ssh.enabled)
                bad.append("server ingress rule without a source: %s" % rule)
    if name.endswith("-steps"):
        for rule in d["spec"].get("egress", []):
            for to in rule.get("to", []):
                if "ipBlock" in to:
                    bad.append("step pods are given an address range by default: %s" % to)
if bad:
    print("\n".join(bad))
    sys.exit(1)
print("ok")
