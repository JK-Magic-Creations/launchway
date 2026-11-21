#!/bin/sh
# Brings up (up) or tears down (down) the lab for the bootc test: a local
# registry, three small operating-system images (b1, b2 good; b3 fails its
# health check) and one throwaway virtual machine booted from a Fedora CoreOS
# disk image under qemu with KVM, all as the user, with no privileges, in
# $LW_LAB (default ~/scratch-vm). The VM has 1 vCPU, 1 GB of memory and a
# sparse 10 GB disk (the image's own size; it uses about 3 GB), user-mode
# networking (no bridge) and ports forwarded on 127.0.0.1 only.
#
#   sh lab.sh up     prints the variables the test reads
#   sh lab.sh down   stops the VM and removes the registry and the disks
set -eu
LAB=${LW_LAB:-$HOME/scratch-vm}
HERE=$(cd "$(dirname "$0")" && pwd)
APP=$HERE/../app
SSHP=${LW_BOOTC_SSH_PORT:-2222}; HTTPP=${LW_BOOTC_HTTP_PORT:-8081}; REGP=${LW_BOOTC_REG_PORT:-5055}
cmd=${1:-}
case $cmd in
up)
  mkdir -p "$LAB/bctx"
  [ -f "$LAB/fcos.qcow2" ] || { echo "put the Fedora CoreOS qemu image (decompressed) at $LAB/fcos.qcow2" >&2; exit 2; }
  podman container exists lw-vmtest-registry || podman run -d --name lw-vmtest-registry -p 127.0.0.1:$REGP:5000 docker.io/library/registry:2 >/dev/null
  for v in "b1 good" "b2 good" "b3 badhealth"; do
    set -- $v; d=$LAB/bctx/$1; mkdir -p "$d"
    (cd "$APP" && CGO_ENABLED=0 go build -o "$d/web" -ldflags "-X main.version=$1 -X main.mode=$2" .)
    cp "$HERE/Containerfile" "$d/"
    (cd "$d" && podman build -q -t localhost:$REGP/lw/os:$1 . >/dev/null)
    podman push -q --tls-verify=false localhost:$REGP/lw/os:$1
  done
  [ -f "$LAB/vmkey" ] || ssh-keygen -q -t ed25519 -N '' -f "$LAB/vmkey"
  PUB=$(cat "$LAB/vmkey.pub")
  cat > "$LAB/a.ign" <<IGN
{"ignition":{"version":"3.4.0"},"passwd":{"users":[{"name":"core","sshAuthorizedKeys":["$PUB"]}]},
 "storage":{"files":[{"path":"/etc/hostname","contents":{"source":"data:,lw-vmtest-a"},"mode":420,"overwrite":true},
 {"path":"/etc/containers/registries.conf.d/99-lw.conf","mode":420,"contents":{"source":"data:,%5B%5Bregistry%5D%5D%0Alocation%20%3D%20%2210.0.2.2%3A$REGP%22%0Ainsecure%20%3D%20true%0A"}}]}}
IGN
  rm -f "$LAB/lw-vmtest-a.qcow2"
  qemu-img create -q -f qcow2 -F qcow2 -b "$LAB/fcos.qcow2" "$LAB/lw-vmtest-a.qcow2"
  qemu-system-x86_64 -name lw-vmtest-a -enable-kvm -cpu host -smp 1 -m 1024 -display none \
    -serial file:"$LAB/a-console.log" -pidfile "$LAB/a.pid" -daemonize \
    -drive file="$LAB/lw-vmtest-a.qcow2",if=virtio -fw_cfg name=opt/com.coreos/config,file="$LAB/a.ign" \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:$SSHP-:22,hostfwd=tcp:127.0.0.1:$HTTPP-:8080 -device virtio-net-pci,netdev=n0
  echo "export LW_BOOTC_SSH=127.0.0.1:$SSHP LW_BOOTC_KEY=$LAB/vmkey LW_BOOTC_HTTP=127.0.0.1:$HTTPP LW_BOOTC_REGISTRY_LOCAL=127.0.0.1:$REGP LW_BOOTC_REGISTRY_GUEST=10.0.2.2:$REGP"
  ;;
down)
  [ -f "$LAB/a.pid" ] && kill "$(cat "$LAB/a.pid")" 2>/dev/null || true
  rm -f "$LAB/a.pid" "$LAB/lw-vmtest-a.qcow2"
  podman rm -f -t 0 lw-vmtest-registry >/dev/null 2>&1 || true
  ;;
*) echo "usage: lab.sh up|down" >&2; exit 2 ;;
esac
