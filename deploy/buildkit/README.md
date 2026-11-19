# BuildKit for image steps

Image steps (`kind: image`) are built by [BuildKit](https://github.com/moby/buildkit), run rootless,
apart from the step pods. The runner is BuildKit's client: it sends the build context from the
run's workspace, answers the daemon's questions for build secrets and registry credentials over the
build's session, receives the image, and pushes and signs it itself. This directory has what an
operator deploys for that:

| File | What |
|---|---|
| `buildkitd.yaml` | A namespace (`launchway-build`), the daemon (a Deployment), its Service and a NetworkPolicy |
| `seccomp-rootless-buildkit.json` | The seccomp profile the daemon runs with: the container runtime's default, plus the system calls a rootless daemon needs to make user namespaces |

## Why not in the step pods

Step pods meet Pod Security "restricted": they run as an ordinary user with no privilege escalation,
no capabilities and the runtime's default seccomp profile. Nothing that builds a Dockerfile works
under that. Tried on a real cluster (kind, Kubernetes 1.37, containerd 2.3), as uid 1000 in a
namespace that enforces "restricted":

| Builder | Result |
|---|---|
| BuildKit rootless (`buildctl-daemonless.sh`) | `[rootlesskit:parent] error: failed to start the child: fork/exec /proc/self/exe: operation not permitted`: the default seccomp profile refuses `clone`/`unshare` of a user namespace without CAP_SYS_ADMIN |
| BuildKit `--rootless` without RootlessKit | `rootless mode requires to be executed as the mapped root in a user namespace` |
| Buildah (`--isolation chroot`, vfs) | `Error during unshare(CLONE_NEWUSER): Operation not permitted` |
| Kaniko | `chown /kaniko/Dockerfile: operation not permitted`: it needs root (and the project is archived) |
| A pod with its own user namespace (`hostUsers: false`) | not available on this test cluster (kind inside rootless Podman: `failed to set effective UID: invalid argument`); untested elsewhere |

Then, in a namespace of its own, the least that makes rootless BuildKit build (a Dockerfile that
adds a user and chowns a file, so other user IDs are really mapped):

| seccomp | allowPrivilegeEscalation | capabilities | Result |
|---|---|---|---|
| Unconfined | false | drop ALL | `newuidmap ... failed: fork/exec /usr/bin/newuidmap: operation not permitted` |
| Unconfined | true | drop ALL | the same: SETUID/SETGID are out of the bounding set |
| RuntimeDefault | true | drop ALL, add SETUID, SETGID | `fork/exec /proc/self/exe: operation not permitted` (seccomp) |
| **Localhost (this profile)** | **true** | **drop ALL, add SETUID, SETGID** | **builds** |
| Localhost (this profile) | false | drop ALL | `newuidmap ... operation not permitted` |

So the daemon needs exactly: privilege escalation allowed (`newuidmap` and `newgidmap` are
set-user-ID helpers), SETUID and SETGID kept in the bounding set, and a seccomp profile that adds
the namespace system calls (`clone`, `unshare`, `setns`, `mount`, `umount2`, `pivot_root` and the
new mount API, `sethostname`, `setdomainname`) to the runtime default, unconditionally rather than
only with CAP_SYS_ADMIN. It runs as uid 1000, never root. That is Pod Security **"baseline"**
(Localhost seccomp profiles and these two capabilities are allowed there), not "restricted", which
is why it lives in a namespace of its own with "baseline" enforced and "restricted" still warned and
audited. Pipeline authors cannot change any of it: a pipeline names no builder address, no image
for the daemon and no security setting.

The daemon also runs with BuildKit's process sandbox off (`noProcessSandbox`): the sandbox mounts a
new `/proc`, which needs `procMount: Unmasked`, which Pod Security forbids. Builds on one daemon then
share its process namespace and can see (and signal) each other's processes. Treat a daemon as one
trust boundary: run one per team or group of projects, and say in the runners' `images.builders`
which projects use which (`projects:`). The runner never gives a build credentials it may not use,
and build secrets are mounted only into the step of the build that asked for them, but a hostile build
running at the same time on the same daemon could reach another build's processes, and through
`/proc` their files and mounted secrets.

## Install

1. **The seccomp profile, on the nodes** that may run the daemon, at
   `/var/lib/kubelet/seccomp/profiles/launchway-rootless-buildkit.json` (copy the file, or manage it
   with the Kubernetes Security Profiles Operator). A dedicated node pool for builds, with a taint
   the daemon tolerates, keeps builds off the nodes that run everything else.
2. **Certificates.** The daemon and the runners know each other by mutual TLS. Make a CA, a server
   certificate for the Service's name (`buildkitd.launchway-build.svc`), and a client certificate
   for the runners, with cert-manager or by hand:

   ```sh
   openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 365 \
     -keyout ca.key -out ca.crt -subj /CN=launchway-buildkit-ca
   openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout tls.key -out tls.csr -subj /CN=buildkitd
   printf 'subjectAltName=DNS:buildkitd.launchway-build.svc\nextendedKeyUsage=serverAuth\n' > server.ext
   openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 365 -out tls.crt -extfile server.ext
   openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout client.key -out client.csr -subj /CN=launchway-runner
   printf 'extendedKeyUsage=clientAuth\n' > client.ext
   openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 365 -out client.crt -extfile client.ext
   kubectl apply -f buildkitd.yaml      # makes the namespace; the pod waits for its Secret
   kubectl -n launchway-build create secret generic buildkitd-tls \
     --from-file=ca.crt --from-file=tls.crt --from-file=tls.key
   kubectl -n launchway create secret generic buildkit-client \
     --from-file=ca.crt --from-file=tls.crt=client.crt --from-file=tls.key=client.key
   ```

   One CA signs both sides here; the daemon trusts any client certificate it signed, so keep its
   key safe and use it for nothing else.
3. **The daemon**: review `buildkitd.yaml` (the runners' namespace in the NetworkPolicy, the egress
   rules for your registries, resources, a PersistentVolumeClaim for the cache) and apply it.
4. **The runners**: with the Helm chart, `runner.images` (see its values): the builders, the
   `tlsSecret` (`buildkit-client` above), the registries with their push rules, and optionally a
   signing key Secret. Image steps go only to runners that have builders.

Registries with a private CA need it twice: in `buildkitd.toml` (the daemon pulls base images and
reads and writes the build cache) and in the runners' `images.registries.<name>.ca_file` (the runner
pushes).

## Multi-platform images

A daemon builds for its own architecture, and for others only through emulation (QEMU, registered
with `binfmt_misc` on the node: a privileged, one-time change to the node that this setup does not
make). Emulated builds are slow and sometimes wrong. The enterprise answer is a daemon per
architecture, on nodes of that architecture: list each in `images.builders` with its `platforms:`,
and a step that asks for `platforms: [linux/amd64, linux/arm64]` is built on both at once and
pushed as one index. A platform no builder can build is refused, naming what the builders build.
