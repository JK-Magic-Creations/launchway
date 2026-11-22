# Shell kit for Launchway custom steps: a small wrapper of the step contract
# (docs/reference/step-contract.md). POSIX sh and awk, nothing to install.
# Copy it into a step (`launchway step new --lang sh` does) and source it:
#
#   . ./launchway-step.sh
#   who=$(lw_input who)
#   lw_output_string greeting "Hello, $who"
#   lw_summary "Greeted **$who**"
#   lw_finish
#
# Scalar inputs (string, number, boolean) are read from the environment; a list
# is printed one item per line by lw_list; records and secrets: lw_secret NAME
# prints a secret input's value.

lw_input() { # lw_input NAME: the value of a string, number or boolean input
  _n=$(printf '%s' "$1" | tr 'abcdefghijklmnopqrstuvwxyz-' 'ABCDEFGHIJKLMNOPQRSTUVWXYZ_')
  eval "printf '%s' \"\${LAUNCHWAY_WITH_$_n-}\""
}

lw_list() { # lw_list NAME: the items of a list input, one per line
  awk -v key="$1" 'BEGIN{RS="\0"} {
    s=$0; i=index(s, "\"" key "\":["); if (!i) exit;
    s=substr(s, i+length(key)+4); out=""; instr=0; esc=0; cur="";
    for (j=1;j<=length(s);j++) { c=substr(s,j,1);
      if (instr) { if (esc) { cur=cur (c=="n"?"\n":c=="t"?"\t":c); esc=0 } else if (c=="\\") esc=1; else if (c=="\"") { print cur; cur=""; instr=0 } else cur=cur c }
      else if (c=="\"") instr=1; else if (c=="]") break }
  }' "$LAUNCHWAY_STEP_INPUTS"
}

lw_secret() { # lw_secret NAME: the value of a secret input
  _v=$(awk -v key="$1" 'BEGIN{RS="\0"} { i=index($0, "\"" key "\":{\"secret\":\""); if (!i) exit; s=substr($0, i+length(key)+14); j=index(s, "\""); print substr(s,1,j-1) }' "$LAUNCHWAY_STEP_INPUTS")
  eval "printf '%s' \"\${$_v-}\""
}

_lw_str() { # a JSON string of the argument
  printf '%s' "$1" | awk 'BEGIN{ORS=""; printf "\""} { if (NR>1) printf "\\n"; n=length($0); for(i=1;i<=n;i++){c=substr($0,i,1); if(c=="\\")printf "\\\\"; else if(c=="\"")printf "\\\""; else if(c=="\t")printf "\\t"; else if(c=="\r")printf "\\r"; else printf "%s", c} } END{printf "\""}'
}

_lw_out=${LW_OUT_TMP:-$(mktemp "${TMPDIR:-/tmp}/lw-out.XXXXXX")}
: > "$_lw_out"

_lw_put() { printf '%s\t%s\n' "$1" "$2" >> "$_lw_out"; }

lw_output_string() { _lw_put "$1" "$(_lw_str "$2")"; }
lw_output_number() { _lw_put "$1" "$2"; }
lw_output_bool() { _lw_put "$1" "$2"; }
lw_output_list() { # lw_output_list NAME ITEM...
  _k=$1; shift; _l="["; _s=""
  for _i in "$@"; do _l="$_l$_s$(_lw_str "$_i")"; _s=","; done
  _put_l="$_l]"; _lw_put "$_k" "$_put_l"
}
lw_output_secret() { lw_mask "$2"; lw_output_string "$1" "$2"; }

lw_finish() { # write the outputs; lw_finish [EXIT CODE]
  if [ -n "${LAUNCHWAY_STEP_OUTPUTS-}" ] && [ -s "$_lw_out" ]; then
    awk -F'\t' 'BEGIN{printf "{"} { if (NR>1) printf ","; k=$1; $1=""; v=substr($0,2); printf "\"%s\":%s", k, v } END{printf "}"}' "$_lw_out" > "$LAUNCHWAY_STEP_OUTPUTS"
  fi
  rm -f "$_lw_out"
  exit "${1:-0}"
}

_lw_result() { [ -n "${LAUNCHWAY_STEP_RESULTS-}" ] && printf '%s\n' "$1" >> "$LAUNCHWAY_STEP_RESULTS"; return 0; }

lw_summary() { _lw_result "{\"t\":\"summary\",\"markdown\":$(_lw_str "$1")}"; }
lw_annotate() { # lw_annotate LEVEL FILE LINE MESSAGE
  _lw_result "{\"t\":\"annotation\",\"level\":$(_lw_str "$1"),\"file\":$(_lw_str "$2"),\"line\":$3,\"message\":$(_lw_str "$4")}"; }
lw_finding() { # lw_finding RULE SEVERITY FILE LINE MESSAGE
  _lw_result "{\"t\":\"finding\",\"rule\":$(_lw_str "$1"),\"severity\":$(_lw_str "$2"),\"file\":$(_lw_str "$3"),\"line\":$4,\"message\":$(_lw_str "$5")}"; }
lw_test() { # lw_test NAME STATUS [MILLIS] [MESSAGE]
  _lw_result "{\"t\":\"test\",\"name\":$(_lw_str "$1"),\"status\":$(_lw_str "$2"),\"duration_ms\":${3:-0},\"message\":$(_lw_str "${4-}")}"; }
lw_mask() { _lw_result "{\"t\":\"mask\",\"value\":$(_lw_str "$1")}"; }
lw_fail() { echo "$1" >&2; lw_finish 1; }
lw_allowed() { echo "$1" >&2; lw_finish 64; }
lw_needs_person() { _lw_result "{\"t\":\"needs_person\",\"message\":$(_lw_str "$1")}"; echo "needs a person: $1" >&2; lw_finish 65; }

# lw_run_tool COMMAND...: run a program the step starts without the variables that let it write results.
lw_run_tool() { env -u LAUNCHWAY_STEP_RESULTS -u LAUNCHWAY_STEP_OUTPUTS "$@"; }
