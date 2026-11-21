#!/bin/sh
# Makes /etc/passwd and /etc/group for a step's numeric user: the init container of a Kubernetes step pod runs this
# in the step's own image (internal/executor/kubernetes_user.go), and scripts/ci/test-like-ci.sh runs it the same way.
# Arguments: directory uid gid name home. PASSWD_IN and GROUP_IN name the files to start from (default: the image's own).
dir=$1 uid=$2 gid=$3 name=$4 home=$5
in_passwd=${PASSWD_IN:-/etc/passwd} in_group=${GROUP_IN:-/etc/group}
: > "$dir/passwd" || exit 1
: > "$dir/group" || exit 1
seen_root=0
if [ -r "$in_passwd" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    n=${line%%:*}; rest=${line#*:*:}; u=${rest%%:*}
    [ "$u" = "$uid" ] && continue
    [ "$n" = "$name" ] && continue
    [ "$u" = 0 ] && seen_root=1
    printf '%s\n' "$line" >> "$dir/passwd"
  done < "$in_passwd"
fi
[ "$seen_root" = 1 ] || printf 'root:x:0:0:root:/root:/sbin/nologin\n' >> "$dir/passwd"
printf '%s:x:%s:%s:Launchway step user:%s:/sbin/nologin\n' "$name" "$uid" "$gid" "$home" >> "$dir/passwd"
seen_root=0
if [ -r "$in_group" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    n=${line%%:*}; rest=${line#*:*:}; g=${rest%%:*}
    [ "$g" = "$gid" ] && continue
    [ "$n" = "$name" ] && continue
    [ "$g" = 0 ] && seen_root=1
    printf '%s\n' "$line" >> "$dir/group"
  done < "$in_group"
fi
[ "$seen_root" = 1 ] || printf 'root:x:0:\n' >> "$dir/group"
printf '%s:x:%s:\n' "$name" "$gid" >> "$dir/group"
