#!/bin/sh
# Runs OpenTofu (default) or Terraform for the plan and apply Maneuvers,
# in the tool's own image. POSIX sh and nothing else: the official images
# have no Python or jq.
#
#   LW_MODE      plan | apply
#   PARAM_TOOL   tofu (default) | terraform
#
# State: when the step was given a state credential (TF_HTTP_ADDRESS, from a
# secret reference launchway:state#<workspace>), a backend override file is
# written so the repository needs no backend block. A repository that declares
# a backend of its own keeps it, and the script says what that costs.
#
# plan  writes .launchway/tofu/plan.bin (the plan file), plan.json (the JSON
#       form Launchway reads) and meta.json (tool, state serial, commit).
# apply refuses unless the plan file is the one the plan step made for THIS
#       commit against the state as it is NOW.
set -eu
umask 077

mode="${LW_MODE:-${PARAM_MODE:-}}"
[ -n "$mode" ] || { echo "launchway: mode is plan or apply" >&2; exit 2; }
tool="${PARAM_TOOL:-tofu}"
dir="${PARAM_DIRECTORY:-.}"
out="$(pwd)/.launchway/tofu"
mkdir -p "$out"
say() { printf 'launchway: %s\n' "$*"; }
die() { printf 'launchway: %s\n' "$*" >&2; exit "${2:-1}"; }

case "$tool" in
  tofu|terraform) ;;
  *) die "tool must be tofu or terraform, not $tool" 2 ;;
esac
command -v "$tool" >/dev/null 2>&1 || die "$tool is not in this image; name an image that has it (the Maneuver's image parameter)" 2
if [ "$tool" = terraform ]; then
  say "Terraform is source-available (BSL), not open source; OpenTofu is the open fork and the default. Check your licence terms before using it."
fi

secdir=$(mktemp -d "${TMPDIR:-/tmp}/lw-tofu.XXXXXX")
trap 'rm -rf "$secdir"' EXIT INT TERM

cd "$dir"
base="${PARAM_PLAN_BASE:-$out}"

# ---- variables: typed inputs and levels as a JSON file, never on the command line ----
varargs=""
if [ -n "${PARAM_VARS:-}" ] && [ "${PARAM_VARS}" != "{}" ]; then
  printf '%s' "$PARAM_VARS" > "$secdir/launchway.auto.tfvars.json"
  varargs="$varargs -var-file=$secdir/launchway.auto.tfvars.json"
fi
for f in ${PARAM_VAR_FILES:-}; do varargs="$varargs -var-file=$f"; done

# ---- plugin cache, keyed by the lock file (the Maneuver's cache: key) ----
export TF_PLUGIN_CACHE_DIR="${TF_PLUGIN_CACHE_DIR:-$(pwd)/.cache/tf-plugins}"
mkdir -p "$TF_PLUGIN_CACHE_DIR"
export TF_IN_AUTOMATION=1 TF_INPUT=0 TF_CLI_ARGS=""

# ---- state backend ----
backend=none
if [ -n "${TF_HTTP_ADDRESS:-}" ]; then
  if grep -Eqs '^[[:space:]]*backend[[:space:]]+"' ./*.tf 2>/dev/null; then
    say "this repository declares a backend of its own, so Launchway's built-in state is not used: Launchway cannot lock, version, show or roll back that state"
    backend=own
  else
    backend=builtin
    printf 'terraform {\n  backend "http" {}\n}\n' > launchway_backend_override.tf
    say "state: the built-in backend for this project, workspace ${PARAM_WORKSPACE:-default}"
  fi
fi

if [ "$backend" = none ] && ! grep -Eqs '^[[:space:]]*backend[[:space:]]+"' ./*.tf 2>/dev/null; then
  say "no state credential: pull request and draft runs get none, so this plan starts from EMPTY state and every resource will look new. Treat it as a check that the code is valid, not as what will change."
fi
say "$tool init"
"$tool" init -input=false -no-color ${PARAM_INIT_ARGS:-} || die "init failed" 3

serial() {
  "$tool" state pull 2>/dev/null | grep -o '"serial": *[0-9]*' | head -1 | grep -o '[0-9]*$' || true
}

case "$mode" in
plan)
  if [ "${PARAM_VALIDATE:-true}" = true ]; then "$tool" validate -no-color || die "validate failed" 3; fi
  cur=$(serial)
  say "planning (state serial ${cur:-none})"
  set +e
  # shellcheck disable=SC2086
  "$tool" plan -input=false -no-color -lock-timeout="${PARAM_LOCK_TIMEOUT:-120s}" -detailed-exitcode ${PARAM_PLAN_ARGS:-} $varargs -out="$base/plan.bin"
  rc=$?
  set -e
  if [ "$rc" -eq 1 ]; then die "plan failed" 1; fi
  "$tool" show -json "$base/plan.bin" > "$base/plan.json"
  ver=$("$tool" version | head -1)
  {
    printf '{"tool":"%s","tool_version":"%s","workspace":"%s","environment":"%s","exit_code":%s,"state_serial":"%s","commit":"%s","state_backend":"%s"}\n' \
      "$tool" "$ver" "${PARAM_WORKSPACE:-default}" "${PARAM_ENVIRONMENT:-}" "$rc" "${cur:-}" "${LAUNCHWAY_COMMIT:-}" "$backend"
  } > "$base/meta.json"
  if [ "$rc" -eq 2 ]; then
    say "the plan has changes"
    if [ "${PARAM_FAIL_ON_CHANGES:-false}" = true ]; then
      die "drift: the infrastructure differs from the code (this run was asked to fail on that)" 2
    fi
  else
    say "no changes: the infrastructure matches the code"
  fi
  ;;
apply)
  [ -f "$base/plan.bin" ] && [ -f "$base/meta.json" ] || die "there is no plan to apply: fetch the plan step's artifact first. Launchway applies a reviewed plan, never a fresh one." 3
  want_commit=$(sed -n 's/.*"commit":"\([^"]*\)".*/\1/p' "$base/meta.json")
  want_serial=$(sed -n 's/.*"state_serial":"\([^"]*\)".*/\1/p' "$base/meta.json")
  if [ -n "$want_commit" ] && [ "$want_commit" != "${LAUNCHWAY_COMMIT:-}" ]; then
    die "stale plan: it was made for commit $want_commit and this run is on ${LAUNCHWAY_COMMIT:-unknown}. Plan again." 4
  fi
  cur=$(serial)
  if [ "${want_serial:-}" != "${cur:-}" ]; then
    die "stale plan: the state changed since the plan was made (serial ${want_serial:-none} then, ${cur:-none} now). Plan again so the review matches what will happen." 4
  fi
  say "applying the reviewed plan (state serial ${cur:-none}, commit ${LAUNCHWAY_COMMIT:-?})"
  set +e
  "$tool" apply -input=false -no-color -lock-timeout="${PARAM_LOCK_TIMEOUT:-120s}" "$base/plan.bin"
  rc=$?
  set -e
  after=$(serial)
  printf '{"tool":"%s","workspace":"%s","environment":"%s","state_serial_before":"%s","state_serial_after":"%s","exit_code":%s,"commit":"%s"}\n' \
    "$tool" "${PARAM_WORKSPACE:-default}" "${PARAM_ENVIRONMENT:-}" "${cur:-}" "${after:-}" "$rc" "${LAUNCHWAY_COMMIT:-}" > "$base/apply-meta.json"
  if [ "$rc" -ne 0 ]; then die "apply failed (exit $rc); the state keeps what was applied" "$rc"; fi
  ;;
*) die "mode is plan or apply, not $mode" 2 ;;
esac
