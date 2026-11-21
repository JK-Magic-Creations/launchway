#!/bin/sh
# Exports the issues and comments of one repository from the server it is on
# into the document `launchway repos import-archive` reads (launchway-archive/1,
# docs/reference/archive-format.md). It uses the source server's REST API, which
# lists issues at /api/v1/repos/OWNER/NAME/issues and their comments at
# .../issues/N/comments, with your own token; nothing is written to that server.
#
#   SOURCE_TOKEN=... scripts/export-issues.sh https://git.example.org OWNER/NAME > issues.json
#   launchway repos import-archive OWNER/NAME issues.json
#
# Needs curl and jq. Flags: --with-pulls also exports the source's pull request
# records (marked is_pull_request), --page-size N (default 50, the source's own
# limit may be lower). The token is read from the environment, never from an
# argument, so it does not show in a process list.
set -eu

with_pulls=false
page_size=50
while [ $# -gt 0 ]; do
	case "$1" in
	--with-pulls) with_pulls=true; shift ;;
	--page-size) page_size="${2:?--page-size needs a number}"; shift 2 ;;
	-h | --help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	--) shift; break ;;
	-*) echo "unknown flag $1" >&2; exit 2 ;;
	*) break ;;
	esac
done
if [ $# -ne 2 ]; then
	echo "usage: SOURCE_TOKEN=... $0 [--with-pulls] [--page-size N] SERVER_URL OWNER/NAME > issues.json" >&2
	exit 2
fi
server=${1%/}
repo=$2
: "${SOURCE_TOKEN:?set SOURCE_TOKEN to a token that may read the repository's issues}"
command -v curl >/dev/null 2>&1 || { echo "curl is needed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq is needed" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
# The token goes in a header file, not on the command line.
printf 'Authorization: token %s\n' "$SOURCE_TOKEN" >"$work/auth"
chmod 600 "$work/auth"

get() { # get PATH -> body on stdout; fails on any status but 200
	curl -fsS --retry 3 -H "@$work/auth" -H 'Accept: application/json' "$server/api/v1/$1"
}

# fetch_all PATH-WITH-QUERY OUTFILE: every page of a list, as one JSON array.
fetch_all() {
	page=1
	: >"$2.parts"
	while :; do
		body=$(get "$1&page=$page&limit=$page_size")
		got=$(printf '%s' "$body" | jq 'length')
		[ "$got" -eq 0 ] && break
		printf '%s\n' "$body" >>"$2.parts"
		[ "$got" -lt "$page_size" ] && break
		page=$((page + 1))
	done
	jq -s 'add // []' "$2.parts" >"$2"
}

kinds="issues"
$with_pulls && kinds="issues pulls"
: >"$work/issues.ndjson"
for kind in $kinds; do
	fetch_all "repos/$repo/issues?state=all&type=$kind" "$work/$kind.json"
	jq -c --arg kind "$kind" '.[] | . + {launchway_kind: $kind}' "$work/$kind.json" >>"$work/issues.ndjson"
done

total=$(wc -l <"$work/issues.ndjson" | tr -d ' ')
echo "exporting $total records of $repo from $server" >&2

: >"$work/out.ndjson"
while IFS= read -r line; do
	n=$(printf '%s' "$line" | jq '.number')
	fetch_all "repos/$repo/issues/$n/comments?" "$work/c$n.json"
	printf '%s' "$line" | jq -c --slurpfile comments "$work/c$n.json" '{
		number: .number,
		title: (.title // ""),
		state: (.state // "open"),
		author: (.user.login // ""),
		body: (.body // ""),
		labels: [(.labels // [])[] | .name],
		milestone: (.milestone.title // ""),
		assignees: [(.assignees // [])[] | .login],
		url: (.html_url // ""),
		is_pull_request: (.launchway_kind == "pulls"),
		created_at: (.created_at // ""),
		updated_at: (.updated_at // ""),
		closed_at: (.closed_at // ""),
		comment_count: ($comments[0] | length),
		comments: [$comments[0][] | {id: (.id | tostring), author: (.user.login // ""), body: (.body // ""), created_at: (.created_at // ""), updated_at: (.updated_at // "")}]
	}' >>"$work/out.ndjson"
done <"$work/issues.ndjson"

jq -s --arg url "$server/$repo" --arg host "$server" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	'{format: "launchway-archive/1", source: {name: $host, url: $url}, exported_at: $at, issues: (. | sort_by(.number))}' "$work/out.ndjson"
echo "done" >&2
