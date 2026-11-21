#!/usr/bin/env python3
"""AT-GAP-9 / S40-12: summarise a window of Gitea access logs.

Read-only. Input is either a Loki endpoint (a `kubectl port-forward` to
svc/loki works) or a file of `kubectl logs` output. Output is a Markdown
report grouped by client, path family and status. Gitea's default log does
not carry the user agent or the signed-in user, so clients are told apart by
source address, and the report says which addresses are only the node
(Cilium's proxy hides the real client of traffic that goes through the
gateway name).

  census.py --loki http://localhost:13100 --days 28 --out census.md
  census.py --file gitea.log --out census.md
  census.py ... --pods pods.txt     # "ip namespace/name" per line, to name clients
"""
import argparse, collections, datetime, json, re, sys, time, urllib.parse, urllib.request

LINE = re.compile(r'completed (\w+) (\S+) for (\S+?):\d+, (\d+) ')


def family(path):
    p = path.split('?')[0]
    parts = p.split('/')
    if p.startswith('/api/actions'):
        return 'actions runner RPC'
    if p.startswith('/api/packages') or '/packages' in p:
        return 'packages'
    if p.startswith('/api/v1/'):
        return 'api ' + '/'.join(parts[3:4] + (parts[6:7] if parts[3:4] == ['repos'] else []))
    if p.endswith('/info/refs'):
        return 'git smart HTTP: refs'
    if p.endswith('git-upload-pack'):
        return 'git smart HTTP: fetch/clone'
    if p.endswith('git-receive-pack'):
        return 'git smart HTTP: push'
    if '/info/lfs' in p or '/media/' in p:
        return 'lfs'
    if '/archive/' in p:
        return 'web archive'
    if '/raw/' in p:
        return 'web raw'
    return 'web other'


def loki_lines(base, days, query):
    end = int(time.time() * 1e9)
    start = end - int(days * 86400 * 1e9)
    seen, cur = set(), end
    while True:
        q = urllib.parse.urlencode({'query': query, 'start': start, 'end': cur, 'limit': 5000, 'direction': 'backward'})
        data = json.load(urllib.request.urlopen(base + '/loki/api/v1/query_range?' + q, timeout=300))
        ents = sorted((int(ts), line) for s in data['data']['result'] for ts, line in s['values'])
        new = [e for e in ents if e not in seen]
        if not ents or not new:
            return
        seen.update(new)
        for e in new:
            yield e
        if ents[0][0] >= cur:
            return
        cur = ents[0][0]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--loki'); ap.add_argument('--file'); ap.add_argument('--pods')
    ap.add_argument('--days', type=float, default=7)
    ap.add_argument('--query', default='{namespace="gitea",container="gitea"} |= "HTTPRequest"')
    ap.add_argument('--out', default='-')
    a = ap.parse_args()
    names = {}
    if a.pods:
        for l in open(a.pods):
            p = l.split()
            if len(p) == 2:
                names[p[0]] = p[1]
    if a.loki:
        src = ((datetime.datetime.fromtimestamp(ts / 1e9, datetime.timezone.utc), l) for ts, l in loki_lines(a.loki, a.days, a.query))
    else:
        src = ((None, l) for l in open(a.file))
    fam, byip, ipfam, st, days = collections.Counter(), collections.Counter(), collections.defaultdict(collections.Counter), collections.defaultdict(collections.Counter), collections.Counter()
    first = last = None
    for ts, l in src:
        m = LINE.search(l)
        if not m:
            continue
        _, path, ip, status = m.groups()
        f = family(path)
        fam[f] += 1; byip[ip] += 1; ipfam[ip][f] += 1; st[ip][status[0] + 'xx'] += 1
        if ts:
            days[ts.strftime('%Y-%m-%d')] += 1; first = min(first or ts, ts); last = max(last or ts, ts)
    out = ['# Gitea traffic census', '']
    if first:
        out.append('Window: %s to %s (%d days with data)' % (first.date(), last.date(), len(days)))
    out += ['', 'Requests: %d' % sum(fam.values()), '', '## By path family', '', '| Family | Requests |', '|---|---|']
    out += ['| %s | %d |' % kv for kv in fam.most_common()]
    out += ['', '## By client address', '', '| Address | Name now | Requests | Status | Main families |', '|---|---|---|---|---|']
    for ip, n in byip.most_common(40):
        out.append('| %s | %s | %d | %s | %s |' % (ip, names.get(ip, '?'), n, dict(st[ip]), ', '.join('%s %d' % kv for kv in ipfam[ip].most_common(3))))
    out += ['', 'Pod addresses are reused: a name is only right for pods that have lived the whole window.']
    text = '\n'.join(out) + '\n'
    if a.out == '-':
        sys.stdout.write(text)
    else:
        open(a.out, 'w').write(text)


if __name__ == '__main__':
    main()
