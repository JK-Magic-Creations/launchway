#!/bin/sh
# Makes the repository the Kubernetes test pipelines run from: a bare Git repository with two
# branches, packed as demo-repo.tar.gz for the git-server ConfigMap (see git-server.yaml).
#
#   main  a quick two-step pipeline that passes an output from one step to the next
#   slow  a pipeline whose middle step takes about 90 seconds, for the failover tests
#
# Usage: deploy/test/make-test-repo.sh OUTPUT_DIR
# The repository is built inside a throwaway Alpine container, so the host needs only Podman.
set -eu

if [ "${1:-}" != "--inside" ]; then
	out=${1:?usage: make-test-repo.sh OUTPUT_DIR}
	mkdir -p "$out"
	out=$(cd "$out" && pwd)
	script=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
	exec podman run --rm -v "$out":/out:Z -v "$script":/make-test-repo.sh:ro,Z \
		docker.io/library/alpine:3.23.8 sh /make-test-repo.sh --inside
fi

apk add --no-cache git >/dev/null
git config --global user.name "Launchway test"
git config --global user.email "test@launchway.invalid"
git config --global init.defaultBranch main

mkdir -p /work/.launchway
cd /work
git init -q
cat > README.md <<'EOF'
A tiny repository for testing the Launchway Helm chart.
EOF
cat > .launchway/pipeline.yaml <<'EOF'
pipeline: k8s-smoke
steps:
  hello:
    image: docker.io/library/busybox:1.37
    run: |
      echo "hello from pod $(hostname)"
      cat README.md
      echo greeting=hi >> "$LAUNCHWAY_OUTPUTS"
  check:
    image: docker.io/library/busybox:1.37
    needs: hello
    run: |
      test "$LAUNCHWAY_NEEDS_HELLO_GREETING" = hi
      echo "output received; running in pod $(hostname)"
EOF
git add -A
git commit -q -m "A quick pipeline"

git checkout -q -b slow
cat > .launchway/pipeline.yaml <<'EOF'
pipeline: k8s-slow
steps:
  prepare:
    image: docker.io/library/busybox:1.37
    run: echo "preparing in pod $(hostname)"
  slow:
    image: docker.io/library/busybox:1.37
    needs: prepare
    run: |
      for i in $(seq 1 90); do echo "tick $i in pod $(hostname)"; sleep 1; done
  finish:
    image: docker.io/library/busybox:1.37
    needs: slow
    run: echo "finished in pod $(hostname)"
EOF
git commit -q -am "A slow pipeline"
git checkout -q main

git clone -q --bare /work /srv/demo.git
git -C /srv/demo.git update-server-info
tar -czf /out/demo-repo.tar.gz -C /srv demo.git
echo "wrote demo-repo.tar.gz"
