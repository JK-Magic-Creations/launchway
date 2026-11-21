#!/usr/bin/env bash
# A reference agent: it uses no model, only the interface every agent uses.
#
#   1. claims the next item labeled agent:docs (a lease it must keep alive),
#   2. clones the repository with its own token and makes a trivial change to
#      the docs on a branch,
#   3. opens a pull request that names the item ("refs KEY-N"),
#   4. links the pull request to the item and asks for a review,
#   5. or hands the item back with a reason when anything fails.
#
# It never approves, merges or finishes anything: the server would refuse, and a
# person (or, where the policy allows it, a different agent) reviews the change.
#
# Set LAUNCHWAY_SERVER, LAUNCHWAY_TOKEN (the agent's token, shown once when the
# agent was registered) and AGENT_REPO (owner/name). The launchway command is
# found on the PATH or in LAUNCHWAY_BIN.
set -euo pipefail

: "${LAUNCHWAY_SERVER:?set LAUNCHWAY_SERVER}" "${LAUNCHWAY_TOKEN:?set LAUNCHWAY_TOKEN}" "${AGENT_REPO:?set AGENT_REPO (owner/name)}"
lw="${LAUNCHWAY_BIN:-launchway}"
export LAUNCHWAY_SERVER LAUNCHWAY_TOKEN
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

json() { python3 -I -c 'import json,sys; d=json.load(sys.stdin); print(d.get(sys.argv[1], ""))' "$1"; }

# 1. Take the next item. Nothing waiting is not a failure.
if ! claimed="$("$lw" work agent claim --json 2>/dev/null)"; then
  echo "nothing is waiting for this agent"
  exit 0
fi
ref="$(printf '%s' "$claimed" | json ref)"
echo "claimed $ref"

give_back() { "$lw" work agent hand-back "$ref" --reason "$1" >/dev/null || true; echo "handed back $ref: $1" >&2; exit 1; }

# 2. Work in a clone with the agent's own credential.
host="${LAUNCHWAY_SERVER#*://}"
scheme="${LAUNCHWAY_SERVER%%://*}"
git clone -q "$scheme://agent:${LAUNCHWAY_TOKEN}@${host}/git/${AGENT_REPO}.git" "$work_dir/repo" || give_back "could not clone the repository"
cd "$work_dir/repo"
git config user.name "docs-agent"
git config user.email "docs-agent@invalid"
branch="work/$(printf '%s' "$ref" | tr 'A-Z' 'a-z')-docs"
git checkout -q -b "$branch"
mkdir -p docs
printf '\n- Reviewed by the docs agent for %s.\n' "$ref" >> docs/CHANGES.md
git add docs/CHANGES.md
git commit -q -m "Docs: note the review of $ref" || give_back "nothing to commit"
git push -q origin "HEAD:refs/heads/$branch" || give_back "the push was refused"
"$lw" work agent heartbeat "$ref" --note "pushed $branch" >/dev/null || give_back "the lease was lost"

# 3 and 4. Open the pull request, link it, report what it cost, ask for a review.
pr_json="$("$lw" pr create -R "$AGENT_REPO" --head "$branch" --title "Docs: note the review of $ref" --body "refs $ref" --json)" || give_back "the pull request could not be opened"
pr_number="$(printf '%s' "$pr_json" | json number)"
"$lw" work items code-link "$ref" --kind pull --code-repo "$AGENT_REPO" --ref "$pr_number" --title "Docs: note the review of $ref" --state open >/dev/null || true
"$lw" work agent usage "$ref" --tokens 0 --cents 0 --seconds 3 --note "scripted: no model" >/dev/null || true
"$lw" work agent request-review "$ref" --summary "Added a line to docs/CHANGES.md (pull request #$pr_number)." >/dev/null || give_back "could not ask for a review"
echo "opened pull request #$pr_number for $ref and asked for a review"
