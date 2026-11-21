# Install Launchway in ten minutes

You need nothing but a place to run it: no secrets manager, no operator, no identity provider, and no
Helm or kubectl. One command asks a few plain questions, shows what it is about to do, does it, and ends
by printing the address and the one-time setup code. Everything after that happens in the browser.

```sh
launchway install
```

It asks first where Launchway should run:

| You choose | What happens | Command you typed |
|---|---|---|
| **On this machine, by itself** (a laptop trial or one server) | A trial starts a server for you only, on `127.0.0.1`, with its data in one folder under your home. A server writes a systemd service under its own account, keeps the root key, and asks how the database and TLS are done. | `launchway install` |
| **On a Kubernetes cluster I already have** | It shows the cluster it found, asks you to type its name, checks what Launchway needs, writes a ten-line values file, installs the chart this program carries, waits, and opens a port-forward until you have made the first administrator. | `launchway install` |
| **Set up a small Kubernetes on this machine for me** | Shown as coming and cannot be chosen yet; see [what is not built yet](#what-is-not-built-yet). | |

Measured on 21 November 2026, from a development build against a fresh `kind` cluster: one command, three
typed answers (the menu choice, the name of the cluster to confirm, and the image, which only a development
build asks for), 53 seconds from pressing Enter to the first signed-in page. A person reading each prompt
will take longer; the work the installer does takes under a minute.

Useful flags: `--dry-run` shows the summary and changes nothing; `--answers FILE` (and `--set key=value`)
answers every question from a file, for scripts; `--uninstall` says what it removes and what it keeps (the
root key and your data, always, unless you say in so many words to delete the data). It writes a transcript
(`launchway-install.log`) that never holds a secret, it is safe to interrupt and run again, and it never makes
a second root key. See [the installer reference](../reference/cli.md#launchway-install).

## On this machine

**A trial** (`this machine`, then `trial`): the questions are where to keep the data (default under
`~/.local/share/launchway`), the port, and whether to use built-in accounts (a one-time setup code makes the
first administrator) or, for a local trial only, no sign-in at all, which is allowed only because the server
listens on `127.0.0.1`. It starts the server in the background; to start it again after a reboot, run the
command it prints. `launchway install --uninstall` stops it and removes what it made, and keeps your data.

**A server** (`this machine`, then `server`, on Linux with systemd): it writes `launchway.service` for an
unprivileged account (`launchway`, made if missing; or your own account as a user service when you are not
root), a data folder with the right permissions, and the root key once, with mode 0600, in `/etc/launchway`.
It asks for the address people will open and how it is protected: behind **your own reverse proxy** (Launchway
listens on this machine only), **a certificate you have** (it prints a ready Caddy configuration that uses
your files), or **plain http on a private network** (with a warning). The database is a PostgreSQL you
already have, **a bundled PostgreSQL run as a container unit** (only offered when Podman or Docker is
present), or the built-in file database for a small installation. When the installer is not root it does not
fail: it prints the exact commands to run as root.

## On Kubernetes

The installer reads your kubeconfig, shows the cluster it would change and asks you to **type its name**. It
then checks, and for each thing that is missing offers a way forward instead of stopping:

| It checks | If it is missing |
|---|---|
| Kubernetes 1.30 or newer | Says so and stops before changing anything: upgrade, or choose a machine install. |
| Rights to create a namespace | Asks for an existing namespace you may install into, and checks you can. |
| A default storage class | Asks for a class by name, or a clearly labelled **trial on temporary storage**, or your own PostgreSQL. |
| A way in (Ingress class, Gateway API) | Offers the ones it found, or a **port-forward** now and says how to set up an address later. |
| TLS | cert-manager issuer, a Secret you have, or plain http on a private network with a warning. |
| A host name | Asks; if its DNS is not ready, the port-forward serves until it is. |
| Network policies enforced by the cluster's network plugin | Says plainly that the shipped lockdown will be installed but will not take effect. |
| Pod security, image pull, room | Reports the namespace's level, explains a pull failure in plain words with the fix (a pull secret, a mirror, `kind load`), warns when the cluster looks short of CPU or memory. |
| Argo CD or Flux | Offers to write the values file and an `Application` or `HelmRelease` for you to commit, and apply nothing. |

It writes `launchway-values.yaml` (about ten lines, no secrets, safe to commit), prints the equivalent
`helm upgrade --install` command, and then installs the chart carried inside the program, so the version
installed always matches the installer. If you would rather run Helm yourself, the next section is that.

## By hand, with Helm

Three commands and one value, the address people will open:

```sh
helm install launchway ./deploy/helm/launchway \
  --namespace launchway --create-namespace \
  --set externalURL=https://launchway.example.com
kubectl -n launchway get secret launchway-setup-code -o jsonpath='{.data.code}' | base64 -d
kubectl -n launchway port-forward svc/launchway 8080:8080    # until your Ingress or Gateway is set up
```

What the chart does by itself:

- A small hook Job creates the secrets the installation needs, each only if it does not exist yet and
  never changed afterwards: the root key that protects stored secrets, the runner token, the bundled
  database's password and the one-time setup code. Nothing is generated while the chart is rendered,
  so `helm template`, `helm upgrade` and a GitOps controller all produce the same result every time.
- The bundled PostgreSQL gets a small volume (set `database.size`, or give an existing database with
  `database.url`).
- Sign-in is on from the first moment (`auth.mode: local`), encryption at rest is on, and default-deny
  network policies cover the servers, the runners and the step pods.

Then open the address, enter the setup code, choose a name and a password of at least 12 characters,
add the authenticator secret to an authenticator app, type the code it shows, and save the ten
recovery codes. The setup code works once and expires after 24 hours. If it has expired, or was lost:

```sh
kubectl -n launchway exec deploy/launchway -- launchway users setup-code
```

The few other values most people set: `database` (a size, or an existing database URL), `replicas`
(high availability) and `ingress` or `httpRoute` (how the address is exposed). The
[values reference](../reference/helm-values.md) lists everything else.

> **The root key.** Helm does not remove the Secret `launchway-root-key` when you uninstall, on
> purpose: without it every stored secret in the database is unreadable. Keep a copy somewhere safe
> and separate (see the [hardening checklist](../security/hardening.md)). Delete it yourself only
> when you are sure you will never need that data again.

## As one binary on one machine

```sh
launchway server --listen 0.0.0.0:8080 --data /var/lib/launchway
```

With no configuration, a server that listens beyond this machine uses built-in accounts and prints a
one-time setup code once, in its start-up message. Open the address and finish setup as above. On
this machine only (`127.0.0.1`), a server without configuration has no sign-in, which is for local
development and is refused on any other address. Lost the code? `launchway users setup-code`, run
on the server, prints a new one.

To keep Launchway running, run it as a service under its own unprivileged user; see
[Install on one server](install-single-server.md).

## After the first sign-in

- Someone locked out: an administrator sends them a one-time reset link, or you run
  `launchway users reset NAME` on the server (there may be no e-mail).
- More people: Settings, People, invite by one-time link.
- An identity provider later: built-in accounts and a provider can both stay on, so switching
  providers cannot lock everyone out. See [Sign-in with OpenID Connect and roles](oidc-and-roles.md).
- Check where you stand: [Hardening checklist](../security/hardening.md).

## What is not built yet

- **A small Kubernetes on this machine** (`launchway install` offering to set up k3s). The design is in
  [the installer's design notes](../development/guided-installer.md): k3s (Apache-2.0) brings a default
  storage class, an Ingress class and a network plugin that enforces policies, which is exactly what the
  installer otherwise has to ask about. It needs root and changes the machine, and it has not been tested in
  a throwaway machine yet, so the menu shows it as coming and does not let you choose it.
- **An offline bundle** (`launchway install bundle --output FILE`, then `launchway install --bundle FILE`):
  one file with the program, the chart and every image, for a network with no internet. Designed in the same
  notes; the command says it is not built yet. Until then, mirror the image into your own registry and set
  `kubernetes.image` to it.
