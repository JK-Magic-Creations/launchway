#!/bin/sh
# Tests the shell kit: sh sdk/steps/sh/test.sh
dir=$(mktemp -d)
printf '{"contract":1,"inputs":{"who":"Ada","files":["a b","c\\"d"],"token":{"secret":"LW_TEST_TOKEN"}}}' > "$dir/inputs.json"
: > "$dir/results.ndjson"
export LAUNCHWAY_STEP_INPUTS="$dir/inputs.json" LAUNCHWAY_STEP_OUTPUTS="$dir/outputs.json" LAUNCHWAY_STEP_RESULTS="$dir/results.ndjson" LAUNCHWAY_WITH_WHO=Ada LW_TEST_TOKEN=s3cr3t
. "$(dirname "$0")/launchway-step.sh"
fail() { echo "FAIL: $1" >&2; exit 1; }
[ "$(lw_input who)" = "Ada" ] || fail "lw_input"
[ "$(lw_list files | sed -n 1p)" = "a b" ] || fail "lw_list first: $(lw_list files)"
[ "$(lw_list files | sed -n 2p)" = 'c"d' ] || fail "lw_list second"
[ "$(lw_secret token)" = "s3cr3t" ] || fail "lw_secret"
lw_output_string greeting 'hi "there"'
lw_output_number count 2
lw_output_bool ok true
lw_output_list names a 'b c'
lw_summary '**done**'
lw_annotate warning a.txt 3 'look'
lw_finding R1 high a.txt 3 'bad'
lw_test t1 passed 12 ''
lw_mask hunter2-value
( lw_finish 0 )
[ "$(cat "$dir/outputs.json")" = '{"greeting":"hi \"there\"","count":2,"ok":true,"names":["a","b c"]}' ] || fail "outputs: $(cat "$dir/outputs.json")"
[ "$(wc -l < "$dir/results.ndjson" | tr -d ' ')" = "5" ] || fail "results: $(cat "$dir/results.ndjson")"
grep -q '"t":"finding","rule":"R1","severity":"high","file":"a.txt","line":3' "$dir/results.ndjson" || fail "finding"
echo ok
