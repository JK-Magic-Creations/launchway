# Install Launchway in ten minutes

You need nothing but a place to run it: no secrets manager, no operator, no identity provider. You
open the address, enter a one-time setup code, create the first administrator, and finish setting up
in the browser. This page has two ways: a Kubernetes cluster with Helm, and a single binary on one
machine.

## On Kubernetes with Helm

Three commands and one value, the address people will open:

```sh
helm install launchway ./deploy/helm/launchway \
  --namespace launchway --create-namespace \
  --set externalURL=https://launchway.example.com
kubectl -n launchway get secret launchway-setup-code -o jsonpath='{.data.code}' | base64 -d
kubectl -n launchway port-forward svc/launchway 8080:80    # until your Ingress or Gateway is set up
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
> and separate (see [Encryption at rest](../guide/encryption-at-rest.md)). Delete it yourself only
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
