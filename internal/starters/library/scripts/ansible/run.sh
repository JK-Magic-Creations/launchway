#!/bin/sh
# Runs one ansible-playbook for the ansible-playbook Maneuver, in the
# execution image the step runs in. Everything arrives as PARAM_* values;
# secrets arrive as environment variables the runner looked up by reference.
#
# Rules this script keeps:
#  - a private key, a vault password and secret variables are written only to
#    a tmpfs directory that is removed when the script ends, mode 0600;
#  - nothing secret is ever put on a command line (it would show in ps);
#  - SSH host keys are pinned: with no known_hosts the script refuses, unless
#    the Maneuver was told host_key_checking: false in so many words;
#  - Launchway's report callback is always on and cannot be switched off by
#    the repository's ansible.cfg (environment beats the file).
set -eu
umask 077

out=.launchway/ansible
mkdir -p "$out"
report="$out/report.json"
rm -f "$report"

# A tmpfs for secrets: /dev/shm, else the runtime directory, else refuse.
secdir=""
for base in /dev/shm "${XDG_RUNTIME_DIR:-}"; do
  [ -n "$base" ] && [ -d "$base" ] && [ -w "$base" ] || continue
  secdir=$(mktemp -d "$base/lw-ansible.XXXXXX") && break
done
if [ -z "$secdir" ]; then
  echo "launchway: no memory-backed directory to keep secrets in; refusing to write them to disk" >&2
  exit 2
fi
trap 'rm -rf "$secdir"' EXIT INT TERM

say() { printf 'launchway: %s\n' "$*"; }

# ---- SSH: key by reference, host keys pinned ----
ssh_args="-o BatchMode=yes -o ConnectTimeout=${PARAM_CONNECT_TIMEOUT:-15}"
if [ -n "${ANSIBLE_SSH_KEY:-}" ]; then
  printf '%s\n' "$ANSIBLE_SSH_KEY" > "$secdir/id"
  chmod 600 "$secdir/id"
  ssh_args="$ssh_args -o IdentitiesOnly=yes -i $secdir/id"
  unset ANSIBLE_SSH_KEY
fi
if [ -n "${PARAM_KNOWN_HOSTS:-}" ]; then
  printf '%s\n' "$PARAM_KNOWN_HOSTS" > "$secdir/known_hosts"
  ssh_args="$ssh_args -o UserKnownHostsFile=$secdir/known_hosts -o StrictHostKeyChecking=yes"
elif [ "${PARAM_HOST_KEY_CHECKING:-true}" = "false" ]; then
  say "host key checking is OFF for this run, as the Maneuver was told; the hosts are not authenticated"
  ssh_args="$ssh_args -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
elif [ "${PARAM_CONNECTION:-ssh}" = "ssh" ]; then
  echo "launchway: no pinned host keys. Give known_hosts (ssh-keyscan -t ed25519 host, checked against the host's own fingerprint) or the run will not connect." >&2
  exit 2
fi
export ANSIBLE_SSH_ARGS="$ssh_args"
export ANSIBLE_HOST_KEY_CHECKING=True

# ---- vault password by reference ----
vault=""
if [ -n "${ANSIBLE_VAULT_PASSWORD:-}" ]; then
  printf '%s' "$ANSIBLE_VAULT_PASSWORD" > "$secdir/vault"
  vault="--vault-password-file $secdir/vault"
  unset ANSIBLE_VAULT_PASSWORD
fi

# ---- extra variables: a file, never the command line ----
# PARAM_EXTRA_VARS is a JSON object (typed inputs and variables by level);
# PARAM_SECRET_VARS lists name=ENVVAR pairs whose values were looked up by
# reference and are in the environment.
python3 - "$secdir/extra.json" <<'PY'
import json, os, sys
doc = {}
raw = os.environ.get("PARAM_EXTRA_VARS", "").strip()
if raw:
    doc.update(json.loads(raw))
for pair in filter(None, os.environ.get("PARAM_SECRET_VARS", "").replace(",", " ").split()):
    name, _, env = pair.partition("=")
    if env not in os.environ:
        sys.exit("launchway: secret variable %s: the environment variable %s was not provided" % (name, env))
    doc[name] = os.environ[env]
json.dump(doc, open(sys.argv[1], "w"))
PY

# ---- inventory ----
inv=""
case "${PARAM_INVENTORY:-}" in
  "") ;;
  inline)
    # PARAM_HOSTS: lines or comma list of  name=address[:port]  (group "all")
    python3 - "$secdir/inventory.json" <<'PY'
import json, os, sys
hosts = {}
for tok in os.environ.get("PARAM_HOSTS", "").replace(",", "\n").split():
    name, _, addr = tok.partition("=")
    h = {}
    if addr:
        a, _, port = addr.partition(":")
        h["ansible_host"] = a
        if port:
            h["ansible_port"] = int(port)
    hosts[name] = h
json.dump({"all": {"hosts": hosts}}, open(sys.argv[1], "w"))
PY
    inv="-i $secdir/inventory.json" ;;
  *) inv="-i ${PARAM_INVENTORY}" ;;
esac
[ -n "${PARAM_USER:-}" ] && export ANSIBLE_REMOTE_USER="$PARAM_USER"

# ---- collections and roles, cached by the hash of requirements.yml ----
req="${PARAM_REQUIREMENTS:-requirements.yml}"
if [ -f "$req" ]; then
  gal="${PARAM_GALAXY_DIR:-.cache/ansible-galaxy}"
  mkdir -p "$gal"
  want=$(sha256sum "$req" | cut -c1-16)-$(ansible --version | head -1 | tr -c 'A-Za-z0-9.\n' '_' | tr -d '\n')
  export ANSIBLE_COLLECTIONS_PATH="$gal/collections" ANSIBLE_ROLES_PATH="$gal/roles${ANSIBLE_ROLES_PATH:+:$ANSIBLE_ROLES_PATH}"
  if [ -f "$gal/.installed" ] && [ "$(cat "$gal/.installed")" = "$want" ]; then
    say "collections and roles are in the cache for $req ($want); not installing again"
  else
    say "installing collections and roles from $req"
    ansible-galaxy collection install -r "$req" -p "$gal/collections" || { echo "launchway: installing collections failed" >&2; exit 2; }
    ansible-galaxy role install -r "$req" -p "$gal/roles" || { echo "launchway: installing roles failed" >&2; exit 2; }
    printf '%s' "$want" > "$gal/.installed"
  fi
fi

# ---- the callback and the flags ----
export ANSIBLE_CALLBACK_PLUGINS="${LAUNCHWAY_LIB:-.}/ansible/callback_plugins${ANSIBLE_CALLBACK_PLUGINS:+:$ANSIBLE_CALLBACK_PLUGINS}"
export ANSIBLE_CALLBACKS_ENABLED=launchway_report
export LAUNCHWAY_ANSIBLE_REPORT="$report"
export ANSIBLE_FORCE_COLOR=0 ANSIBLE_NOCOLOR=1
[ -n "${PARAM_FORKS:-}" ] && export ANSIBLE_FORKS="$PARAM_FORKS"

flags=""
[ "${PARAM_CHECK:-false}" = "true" ] && flags="$flags --check"
[ "${PARAM_DIFF:-false}" = "true" ] && flags="$flags --diff"
[ "${PARAM_BECOME:-false}" = "true" ] && flags="$flags --become"
[ -n "${PARAM_BECOME_USER:-}" ] && flags="$flags --become-user $PARAM_BECOME_USER"
[ -n "${PARAM_LIMIT:-}" ] && flags="$flags --limit $PARAM_LIMIT"
[ -n "${PARAM_TAGS:-}" ] && flags="$flags --tags $PARAM_TAGS"
[ -n "${PARAM_SKIP_TAGS:-}" ] && flags="$flags --skip-tags $PARAM_SKIP_TAGS"

say "ansible-playbook ${PARAM_PLAYBOOK:?playbook is required}${PARAM_CHECK:+ (check=$PARAM_CHECK)}${PARAM_LIMIT:+ limit=$PARAM_LIMIT}"
set +e
# shellcheck disable=SC2086
ansible-playbook $inv $vault -e "@$secdir/extra.json" $flags "$PARAM_PLAYBOOK"
rc=$?
set -e
if [ ! -f "$report" ]; then
  say "no run report was written (the playbook did not start, or it was stopped)"
fi
exit $rc
