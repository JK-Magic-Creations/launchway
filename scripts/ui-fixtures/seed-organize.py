#!/usr/bin/env python3
"""Seeds a believable large Launchway installation for the browser checks of the organizing pages.

  scripts/ui-fixtures/seed-organize.sh WORKDIR     (starts a server there, seeds it, leaves it running; prints its address and PID)

This file does the seeding against a running server:
  seed-organize.py URL DATA_DIR

It makes 8 Squadrons holding about 120 projects (every project reads one tiny local repository), explicit
folders and labels, about 25 hosted repositories, and writes run history straight into the database the way
the engine would have: most pipelines pass; 8 are truly failing; 6 failed and are now being retried; 5 failed
and then passed; 3 failed on someone else's feature branch; 10 are running; 2 wait for a decision. Names are
invented. Standard library only.
"""
import json, os, random, sqlite3, sys, time, urllib.request

url, data = sys.argv[1].rstrip("/"), sys.argv[2]
random.seed(7)
REPO = os.path.abspath(os.path.join(data, "..", "repos", "demo"))  # an absolute path: every project reads this one tiny repository
now = int(time.time() * 1000)
MIN, HOUR = 60_000, 3_600_000

def call(method, path, body=None):
    req = urllib.request.Request(url + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", "X-Launchway-CSRF": "1"})
    with urllib.request.urlopen(req, timeout=60) as r:
        text = r.read().decode()
        return json.loads(text) if text else None

squadrons = {
    "payments": ["ledger", "checkout", "refunds", "invoicing", "fraud-rules", "payouts", "tax-engine", "card-vault", "billing-portal", "reconcile", "fx-rates", "dispute-desk", "subscriptions", "pricing", "wallet"],
    "storefront": ["web-shop", "cart", "search", "recommendations", "catalog", "promo-engine", "reviews", "image-resizer", "seo-pages", "wishlist", "gift-cards", "loyalty", "storefront-docs", "mobile-web", "ab-tests"],
    "platform": ["api-gateway", "auth", "rate-limiter", "feature-flags", "config-service", "event-bus", "scheduler", "secrets-sync", "audit-sink", "service-mesh", "deploy-tools", "cli", "sdk-go", "sdk-js", "status-page"],
    "data": ["warehouse-loader", "metrics-agent", "etl-orders", "etl-catalog", "dashboards", "lineage", "data-quality", "ml-features", "ml-ranker", "ab-stats", "exports", "retention", "pii-scrubber", "streaming", "reports"],
    "mobile": ["ios-app", "android-app", "push-service", "deeplinks", "offline-sync", "app-config", "analytics-sdk", "design-kit", "release-train", "beta-portal", "crash-triage", "store-assets", "widgets", "wear-app", "localization"],
    "logistics": ["routing", "warehouse-api", "label-printing", "carrier-sync", "returns", "inventory", "pick-lists", "dock-scheduler", "tracking", "eta-model", "yard-view", "cold-chain", "customs", "packaging", "forecast"],
    "support": ["helpdesk", "chat-widget", "knowledge-base", "macros", "sla-timers", "csat", "agent-desktop", "voice-gateway", "translations", "refund-bot", "escalations", "ticket-export", "community", "status-feed", "outage-notify"],
    "security": ["sso-bridge", "scanner", "sbom-store", "policy-engine", "vault-sync", "cert-rotation", "dlp", "pentest-tracker", "waf-rules", "key-escrow", "access-reviews", "honeypots", "threat-feed", "mdm", "hardening"],
}
names = [(sq, f"{sq}-{n}") for sq, ns in squadrons.items() for n in ns]
unit_display = {k: k.capitalize() for k in squadrons}

# 1. Configuration: the Wing (organization), the Squadrons and the projects in them.
objs = [{"kind": "organisation", "name": "default", "spec": {"display_name": "Northwind Retail", "description": "Demo organization"}}]
for k, d in unit_display.items():
    objs.append({"kind": "unit", "name": k, "spec": {"display_name": d, "description": f"The {d} Squadron"}})
for sq, n in names:
    objs.append({"kind": "project", "name": n, "unit": sq, "spec": {"repo": REPO, "branch": "main", "poll": "24h"}})
import io
def yml(o, ind=0):
    pad = "  " * ind
    if isinstance(o, dict):
        return "".join(f"{pad}{k}: {yml(v, ind + 1) if isinstance(v, (dict, list)) else json.dumps(v)}\n" if not isinstance(v, (dict, list)) else f"{pad}{k}:\n{yml(v, ind + 1)}" for k, v in o.items())
    if isinstance(o, list):
        out = ""
        for it in o:
            body = yml(it, ind + 1)
            out += f"{pad}- " + body.lstrip() if isinstance(it, dict) else f"{pad}- {json.dumps(it)}\n"
        return out
    return json.dumps(o)
text = "objects:\n" + yml(objs, 1)
res = call("POST", "/api/v1/config/import", {"text": text, "apply": True, "origin": "seed-organize"})
print("config imported:", (res or {}).get("applied", res) if isinstance(res, dict) else res, file=sys.stderr)
time.sleep(3)

# 2. History, written to the database.
db = sqlite3.connect(f"{data}/launchway.db", timeout=60)
db.execute("PRAGMA busy_timeout=60000")
cols = ("id", "project", "number", "pipeline", "status", "event", "ref", "branch", "sha", "actor", "message", "created_at", "started_at", "finished_at", "last_seq")
msgs = ["Round prices per line item", "Bump the base image", "Fix flaky test in retry logic", "Add the discount code endpoint", "Handle empty carts", "Speed up the index build", "Tidy the logging", "Update dependencies", "Cache the rates lookup", "Fix a typo in the docs"]
actors = ["sam.rivera", "priya.nair", "li.wei", "jordan.blake", "ana.souza", "omar.haddad"]
seq = {}
rid = [0]
def add(project, status, ago_min, branch="main", actor=None, step=None, waiting=False, event="push", pr=0):
    seq[project] = seq.get(project, 0) + 1
    rid[0] += 1
    created = now - ago_min * MIN
    started = created + 20_000
    fin = 0 if status in ("running", "queued") else started + random.randint(90, 600) * 1000
    ref = f"refs/pull/{pr}/head" if pr else f"refs/heads/{branch}"
    ev = "pull_request" if pr else event
    row = (f"run_{rid[0]:06d}", project, seq[project], project, status, ev, ref, branch, f"{random.getrandbits(40):010x}", actor or random.choice(actors), random.choice(msgs), created, started if status != "queued" else 0, fin, 0)
    db.execute(f"INSERT INTO runs({','.join(cols)}) VALUES({','.join('?' * len(cols))})", row)
    steps = ["build", "unit-tests", "package", "approve-prod"]
    for i, s in enumerate(steps):
        st = "succeeded"
        if status == "failed" and i == 1: st = "failed"
        elif status == "failed" and i > 1: st = "skipped"
        elif status == "running": st = "succeeded" if i < 1 else ("running" if i == 1 else "pending")
        if waiting: st = "succeeded" if i < 3 else "waiting"
        if status == "succeeded" and i == 3: st = "skipped"
        if status == "queued": st = "pending"
        db.execute("INSERT INTO steps(run_id,step_id,position,status,attempt,queued_at,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?)", (row[0], s, i, st, 1, created, started, fin))
    return row[0]

pool = [n for _, n in names]
random.shuffle(pool)
truly = pool[0:8]; retried = pool[8:14]; healed = pool[14:19]; feature_fail = pool[19:22]; running = pool[22:28]; waiting = pool[28:30]
special = set(truly + retried + healed + feature_fail + running + waiting)
for _, n in names:
    # About a dozen older results, mostly passing.
    t = 4 * 24 * 60
    for i in range(random.randint(9, 16)):
        t -= random.randint(60, 480)
        add(n, "failed" if random.random() < 0.12 else "succeeded", max(t, 400))
    if n not in special:
        add(n, "succeeded", random.randint(20, 300))
        continue
    if n in truly:
        add(n, "succeeded", 700); add(n, "failed", random.randint(30, 220), actor="priya.nair")
    elif n in retried:
        add(n, "succeeded", 900); add(n, "failed", random.randint(30, 60)); add(n, "running", random.randint(2, 9), step="unit-tests", actor=random.choice(["sam.rivera", "li.wei"]))
    elif n in healed:
        add(n, "failed", random.randint(90, 200)); add(n, "succeeded", random.randint(10, 60))
    elif n in feature_fail:
        add(n, "succeeded", 80); add(n, "failed", 30, branch="feature/new-flow", actor="omar.haddad")
    elif n in running:
        add(n, "succeeded", 120); add(n, "running", random.randint(1, 25), actor=random.choice(["sam.rivera", "ana.souza", "li.wei"]))
    elif n in waiting:
        add(n, "succeeded", 150); add(n, "running", 40, actor="sam.rivera", waiting=True)
add(pool[40], "queued", 33, actor="li.wei")  # a Sortie stuck in the queue
db.commit()
db.close()

# 3. Folders and labels, as the people of the installation would have made them.
def folder(area, name, parent=""):
    return call("POST", f"/api/v1/organize/{area}/folders", {"scope": "base", "name": name, "parent": parent})["id"]
core, customer, internal = folder("pipelines", "Core services"), folder("pipelines", "Customer facing"), folder("pipelines", "Internal tools")
tier1 = folder("pipelines", "Tier 1", core)
for _, n in names:
    if any(w in n for w in ("ledger", "checkout", "auth", "api-gateway", "payouts", "routing")):
        call("POST", "/api/v1/organize/pipelines/move", {"item": n, "folder": tier1}); call("PUT", "/api/v1/organize/pipelines/labels", {"item": n, "labels": ["tier-1", "go"]})
    elif any(w in n for w in ("web", "cart", "search", "catalog", "ios", "android", "chat")):
        call("POST", "/api/v1/organize/pipelines/move", {"item": n, "folder": customer}); call("PUT", "/api/v1/organize/pipelines/labels", {"item": n, "labels": ["tier-2", "typescript"]})
    elif any(w in n for w in ("cli", "tools", "dashboards", "sdk", "reports", "exports")):
        call("POST", "/api/v1/organize/pipelines/move", {"item": n, "folder": internal}); call("PUT", "/api/v1/organize/pipelines/labels", {"item": n, "labels": ["tier-3"]})
for n in ("payments-ledger", "storefront-web-shop", "platform-auth"):
    call("POST", "/api/v1/me/favorites", {"item": f"project:{n}", "on": True})

# 3b. The person's trail (what they opened and started) and two panels on their own Home.
runs = call("GET", "/api/v1/runs?limit=60")
mine = [r for r in runs if r["actor"] == "sam.rivera"][:3]
for r in mine:
    call("PUT", "/api/v1/activity", {"kind": "run", "obj": r["id"], "title": f"{r['project']} #{r['number']}", "href": f"/runs/{r['id']}", "action": "started", "state": "running"})
for n in ("payments-ledger", "storefront-web-shop", "platform-auth"):
    call("PUT", "/api/v1/activity", {"kind": "pipeline", "obj": n, "project": n, "title": f"History · {n}", "href": f"/projects/{n}/pipeline?view=history", "action": "viewed"})
call("PUT", "/api/v1/me/layouts/home", {"panels": [
    {"id": "p:runbook", "type": "note", "title": "On-call runbook", "size": 1, "settings": {"text": "**Paging order:** Payments, then Platform.\n\n- [Incident checklist](https://example.com/incidents)\n- [Status page](https://example.com/status)"}},
    {"id": "p:failing", "type": "pinned", "title": "Failing pipelines", "size": 1, "settings": {"href": "/pipelines?result=bad"}},
    {"id": "p:ledger", "type": "project", "title": "Ledger", "size": 1, "settings": {"project": "payments-ledger"}}],
    "order": ["needs", "running", "favorites", "p:runbook", "p:failing", "p:ledger", "continue", "categories", "recent"]})

# 4. Hosted repositories by owner.
for owner, rs in {"payments": ["ledger", "checkout", "refunds", "invoicing"], "storefront": ["web-shop", "cart", "search", "catalog", "promo-engine"], "platform": ["api-gateway", "auth", "sdk-go", "sdk-js", "cli"],
                  "data": ["warehouse-loader", "etl-orders", "ml-ranker"], "docs": ["handbook", "runbooks", "adr"]}.items():
    for r in rs:
        try: call("POST", "/api/v1/repositories", {"owner": owner, "name": r, "visibility": "private" if owner in ("payments", "platform") else "internal"})
        except Exception as e: print("repository", owner, r, e, file=sys.stderr)
print(f"seeded {len(names)} projects, {rid[0]} Sorties", file=sys.stderr)
