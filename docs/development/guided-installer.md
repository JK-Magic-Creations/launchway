# The guided installer: how it is built, and what is designed but not built

`launchway install` (internal/installer, internal/installer/kube, cmd/launchway/install.go) is the owner's
"one command, plain questions" installer (design rulings of 21 November 2026, section 27). This page records
how it works, what it assumes about the machine (nothing), what it measured, and the two parts that are
designed and shown as "coming" in the menu: a small Kubernetes on the machine, and the offline bundle.

## How it is built

- **Questions.** Every question has a key (`target`, `local.mode`, `kubernetes.confirm` ...), a default and, for a
  choice, a list of options. An option that cannot work now carries the reason and cannot be picked, in the menu
  or in an answers file. The same flow runs from a terminal, from `--answers FILE` and from `--set`. A question
  with no answer and no default stops an unattended run and names the key. An answers key nothing read is
  reported afterwards (a typo is not silently ignored). Answers files refuse keys that look like secrets
  (`token`, `password`, `secret`, `passphrase`) unless they end in `_file` or `_env`: a credential is never
  written into a plain file by the installer.
- **Everything the installer touches** goes through an `Env` (files under a root, commands, the network), so
  the tests run every "nothing is present" case with fakes: no systemd, no container engine, not root, no
  cluster, no storage, no ingress, a cluster too old, no rights.
- **A summary before any change**, then the steps; each step is safe to repeat, so an interruption is finished
  by running the installer again. Keys, passwords and setup codes are written only if absent (`O_EXCL`), never
  regenerated. The transcript redacts registered secrets, setup codes, tokens and credentials in URLs.
- **Kubernetes without Helm or kubectl.** The chart is embedded (`deploy/helm/embed.go`), read with Helm's
  chart loader and rendered with Helm's template engine (`helm.sh/helm/v3` packages `chart/loader`, `chartutil`
  and `engine`, Apache-2.0), then applied with the Kubernetes client (`client-go`, Apache-2.0) by server-side
  apply. Hooks are run in weight order (the chart's hook Job makes the secrets once). Objects are stamped as a
  Helm release's, so `helm upgrade` adopts them later. Helm's `action` package was not used: it pulls
  `k8s.io/kubectl`, which needs Kubernetes libraries older than the ones this repository builds with.
- **Size.** Embedding the Helm engine adds about 3 MB (116.6 MB to 119.7 MB for the unstripped development
  build; the release build is stripped and is smaller). That fits the existing size gates, so there is no
  separate `launchway-install` binary: `launchway install` is part of the one program.

## Measured

| | |
|---|---|
| Command typed | 1 (`launchway install`) |
| Answers typed, kind cluster, development build | 3 (the menu choice, the cluster's name, the image) |
| Minutes to the first signed-in page | 0.9 (53 s) with the image already loaded into kind |
| Laptop trial | 1 command, 0 typed answers when every default is accepted, under a second to a running server |

## Designed, not built: a small Kubernetes on this machine

*Evaluated: k3s (Apache-2.0).* One binary and a systemd service; it brings the things the installer otherwise
asks about: a default storage class (local-path), an Ingress class (Traefik), and a network plugin that
enforces `NetworkPolicy` (an embedded kube-router controller), so the shipped lock-down takes effect. It
needs root and ports 6443 and 10250, and it changes the machine, so it is built last and tested only in a
throwaway virtual machine or privileged container.

The flow: the installer states what it will do (install k3s version X, verified by its published SHA-256, as a
service; write `/etc/rancher/k3s/config.yaml` with `write-kubeconfig-mode: "0600"` and `secrets-encryption:
true`); runs the pinned release's binary and its air-gap images (from an offline bundle when there is no
internet); waits for the node to be Ready; reads `/etc/rancher/k3s/k3s.yaml` (root only, so the step needs
root and says so, printing the commands when it is not root); and then runs the Kubernetes flow above against
that cluster, which now has storage, a way in and policy enforcement. `launchway install --uninstall` runs k3s's
own `k3s-uninstall.sh` only after naming it and asking. Until this is built and tested on a throwaway machine,
the menu entry is labelled coming and is not selectable.

## Designed, not built: the offline bundle

One file (a tar of an OCI layout) holding this program, the chart (already inside the program), every image
the release needs (the Launchway image, the PostgreSQL image, and for a small Kubernetes the k3s air-gap
images) and `SHA256SUMS`, signed like a release. `launchway install bundle --output FILE` pulls the images with
`go-containerregistry` (Apache-2.0, already in the module graph) and writes the layout; `launchway install
--bundle FILE` verifies the sums and either pushes the images to a registry the person names (and points the
values file at it) or loads them into the node's container runtime. The command exists and says it is not built
yet, rather than pretending.

## What the installer assumes about the machine

Nothing. A missing tool, right or feature is named, with what to do, and the installer never ends in a dead end:
no `helm` and no `kubectl` are needed; a machine with no systemd is told so and offered the trial or a cluster;
no container engine hides the bundled PostgreSQL choice with the reason; no root prints the commands.
