#!/bin/sh
# Makes /etc/passwd and /etc/group for a step's numeric user: the init container of a Kubernetes step pod runs this
# in the step's own image (internal/executor/kubernetes_user.go), and scripts/ci/test-like-ci.sh runs it the same way.
# Arguments: directory uid gid name home [prefix]. The files are <directory>/<prefix>passwd and <directory>/<prefix>group.
# The directory must exist and be writable: in a pod it is the root of an empty volume, and the prefix (the container's
# name and a dot) keeps one container's files apart from another's without needing a directory nobody has made (this
# script uses only the shell's own commands, so it cannot make one). PASSWD_IN and GROUP_IN name the files to start
# from (default: the image's own).
dir=$1 uid=$2 gid=$3 name=$4 home=$5 pre=${6:-}
in_passwd=${PASSWD_IN:-/etc/passwd} in_group=${GROUP_IN:-/etc/group}
: > "$dir/${pre}passwd" || exit 1
: > "$dir/${pre}group" || exit 1
seen_root=0
if [ -r "$in_passwd" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    n=${line%%:*}; rest=${line#*:*:}; u=${rest%%:*}
    [ "$u" = "$uid" ] && continue
    [ "$n" = "$name" ] && continue
    [ "$u" = 0 ] && seen_root=1
    printf '%s\n' "$line" >> "$dir/${pre}passwd"
  done < "$in_passwd"
fi
[ "$seen_root" = 1 ] || printf 'root:x:0:0:root:/root:/sbin/nologin\n' >> "$dir/${pre}passwd"
printf '%s:x:%s:%s:Launchway step user:%s:/sbin/nologin\n' "$name" "$uid" "$gid" "$home" >> "$dir/${pre}passwd"
seen_root=0
if [ -r "$in_group" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    n=${line%%:*}; rest=${line#*:*:}; g=${rest%%:*}
    [ "$g" = "$gid" ] && continue
    [ "$n" = "$name" ] && continue
    [ "$g" = 0 ] && seen_root=1
    printf '%s\n' "$line" >> "$dir/${pre}group"
  done < "$in_group"
fi
[ "$seen_root" = 1 ] || printf 'root:x:0:\n' >> "$dir/${pre}group"
printf '%s:x:%s:\n' "$name" "$gid" >> "$dir/${pre}group"
