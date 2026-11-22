#!/bin/sh
# Static analysis for insecure code, with bandit, on the project's own source: never the virtual environment,
# installed packages, build output or vendored code (PARAM_ANALYSIS_EXCLUDE, comma separated, names are matched at any
# depth). Every finding of medium severity or worse is written as SARIF (reports/bandit.sarif) so that it lands in
# the findings backlog; the step fails when a finding is at or above PARAM_ANALYSIS_SEVERITY and
# PARAM_ANALYSIS_CONFIDENCE (low, medium or high; default high and high), unless PARAM_ANALYSIS_MODE is report.
# PARAM_ANALYSIS_PATHS limits it to some directories or files (comma separated).
set -e
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
. "$(venv_dir)/bin/activate"
command -v bandit >/dev/null 2>&1 || py_install "bandit[sarif]==1.9.4"
python -c "import sarif_om" 2>/dev/null || py_install "bandit[sarif]==1.9.4"
rd=$(report_dir)
mkdir -p "$rd"
excl=${PARAM_ANALYSIS_EXCLUDE:-tests,.venv,.cache}
sev=${PARAM_ANALYSIS_SEVERITY:-high}
conf=${PARAM_ANALYSIS_CONFIDENCE:-high}
roots=${PARAM_ANALYSIS_PATHS:-.}
# The files to read: Python files under the roots, minus any directory named in the exclusions (at any depth) and the
# directories every Python tool leaves behind (environments, caches, build output, node_modules, vendored code).
prune="-name .git -o -name node_modules -o -name site-packages -o -name __pycache__ -o -name build -o -name dist -o -name vendor -o -name third_party -o -name '.venv*' -o -name 'venv' -o -name '.tox' -o -name reports"
for e in $(printf '%s' "$excl" | tr ',' ' '); do prune="$prune -o -name '$e'"; done
list=$(lw_tools)/analysis-files.txt
mkdir -p "$(dirname "$list")"
: > "$list"
for r in $(printf '%s' "$roots" | tr ',' ' '); do
  if [ -f "$r" ]; then echo "$r" >> "$list"; continue; fi
  eval "find '$r' \\( $prune \\) -prune -o -type f -name '*.py' -print" >> "$list"
done
n=$(wc -l < "$list")
if [ "$n" -eq 0 ]; then say "no Python source to analyse in ${roots} (excluding $excl)"; exit 0; fi
say "analysing $n Python files of the project (not the virtual environment or third-party code); failing at severity $sev and confidence $conf"
rc=0
# shellcheck disable=SC2046
echo "+ bandit -f sarif -o $rd/bandit.sarif -ll <$n files>"
bandit -q -ll -f sarif -o "$rd/bandit.sarif" $(cat "$list") >/dev/null 2>"$rd/bandit.err" || rc=$?
# 0: nothing found, 1: something found; anything else is bandit failing.
if [ "$rc" -gt 1 ] || [ ! -s "$rd/bandit.sarif" ]; then cat "$rd/bandit.err"; echo "bandit did not produce a report (exit $rc)"; exit 1; fi
rc=0
python - "$rd/bandit.sarif" "$sev" "$conf" <<'PY' || rc=$?
import json, sys
rank = {"low": 1, "medium": 2, "high": 3}
doc = json.load(open(sys.argv[1]))
sev, conf = rank[sys.argv[2]], rank[sys.argv[3]]
results = [r for run in doc.get("runs", []) for r in run.get("results", [])]
def prop(r, k): return str(r.get("properties", {}).get(k, "")).lower()
hit = [r for r in results if rank.get(prop(r, "issue_severity"), 0) >= sev and rank.get(prop(r, "issue_confidence"), 0) >= conf]
print(f"launchway: bandit recorded {len(results)} findings of medium severity or worse; {len(hit)} at or above {sys.argv[2]} severity and {sys.argv[3]} confidence")
for r in hit[:50]:
    loc = r["locations"][0]["physicalLocation"]
    print(f"  {prop(r, 'issue_severity')}/{prop(r, 'issue_confidence')}  {loc['artifactLocation']['uri']}:{loc.get('region', {}).get('startLine', '?')}  {r.get('ruleId', '')}: {r.get('message', {}).get('text', '')}")
sys.exit(1 if hit else 0)
PY
if [ "$rc" != 0 ] && [ "${PARAM_ANALYSIS_MODE:-enforce}" = report ]; then
  say "bandit reported findings (analysis_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
