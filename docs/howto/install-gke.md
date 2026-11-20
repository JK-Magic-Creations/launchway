# Install Launchway on Google GKE

This guide runs Launchway on Google Kubernetes Engine with the Helm chart: servers spread across
zones, Cloud SQL for PostgreSQL as the shared database, artifacts in Cloud Storage, the UI behind a
GKE Gateway, and step pods on a node pool of their own. It is for operators who have read
[Install on Kubernetes](install-kubernetes.md), which explains the chart itself.

> **Verified** on 2026-11-19 against commit f717337: `helm lint` and `helm template` of
> `deploy/helm/launchway/examples/gke.yaml` (the values file below), and `helm install` of the same
> file on a local kind cluster with only what cannot exist there replaced (the image, the database
> URL, the S3 endpoint pointed at MinIO, the step namespace, no node selector, no HTTPRoute because
> kind had no Gateway API): three servers and three runners became ready, and the servers accepted
> `region: auto` and started with the S3 store. **Not verified:** nothing was run on Google Cloud.
> Not tested: GKE itself, Cloud SQL (including `sslmode=verify-ca` with its server CA), Cloud
> Storage through its XML API with HMAC keys, the GKE Gateway and its policies, Artifact Registry,
> node pools, and Vault Kubernetes auth from GKE.

## What you need

- A GKE cluster (Standard or Autopilot) on Kubernetes 1.30 or later, with the Gateway API enabled,
  `kubectl` for it, and Helm 3.8 or later.
- An Artifact Registry repository for the Launchway image, and Podman or Docker to build it.
- A Cloud SQL for PostgreSQL instance with a private IP in the cluster's VPC.
- A Cloud Storage bucket for artifacts, and a service account with an HMAC key that may use it.
- An OpenID Connect provider for sign-in (see [Sign-in with OpenID Connect and roles](oidc-and-roles.md)).
- Optional: a node pool for step pods, and Vault or OpenBao for secrets.

## The layout

```mermaid
flowchart LR
  users["People"] --> gw["GKE Gateway (HTTPS)"]
  gw --> servers["Servers, 3 across zones"]
  runners["Runners"] --> servers
  servers --> sql[("Cloud SQL for PostgreSQL")]
  servers --> gcs[("Cloud Storage bucket")]
  runners --> gkeapi["GKE API"]
  gkeapi --> steps["Step pods on the builds node pool"]
  runners --> vault["Vault"]
```

Only the servers use the database and the bucket. Runners talk to the servers, the Kubernetes API,
your Git host and your secret stores, all outbound.

## Build and push the image

Build the image from a clone of the repository and push it to Artifact Registry:

```sh
make image IMAGE=localhost/launchway:0.1.0
gcloud auth print-access-token | \
  podman login --username oauth2accesstoken --password-stdin europe-west1-docker.pkg.dev
podman tag localhost/launchway:0.1.0 europe-west1-docker.pkg.dev/acme-ci/launchway/launchway:0.1.0
podman push europe-west1-docker.pkg.dev/acme-ci/launchway/launchway:0.1.0
```

The nodes' service account needs the Artifact Registry Reader role on that repository. Pin the
image by digest in production with `image.digest`.

## The database

Create a database and a user for Launchway on the Cloud SQL instance. The user must be able to
create tables in that database: servers apply schema migrations when they start. All servers use
the same database and agree through it. Turn on high availability (regional) for the instance if
you want the database to survive a zone outage too.

The servers connect to the instance's private IP directly; the chart has no place for the Cloud SQL
Auth Proxy as a sidecar. Download the instance's server CA certificate (`server-ca.pem`) and give it
to the servers in a ConfigMap. Use `sslmode=verify-ca`: the certificate is checked against that CA,
but not against the IP address.

```sh
kubectl create namespace launchway
kubectl -n launchway create configmap launchway-db-ca --from-file=server-ca.pem
kubectl -n launchway create secret generic launchway-database \
  --from-literal=url='postgres://launchway:PASSWORD@10.20.0.3:5432/launchway?sslmode=verify-ca&sslrootcert=/etc/launchway-db-ca/server-ca.pem'
```

Back the database up with Cloud SQL's automated backups and point-in-time recovery; see
[Back up, restore and upgrade](backup-restore-upgrade.md).

## Artifact storage in Cloud Storage

Launchway's artifact store speaks the S3 API, signed with AWS Signature Version 4. Cloud Storage
offers an S3-compatible XML API at `https://storage.googleapis.com` that accepts such requests when
they are signed with an [HMAC key](https://cloud.google.com/storage/docs/authentication/hmackeys).
Launchway uses only plain object requests and multipart uploads (create, upload part, complete,
abort), which that API documents.

> [!WARNING]
> Launchway's store is tested against MinIO, not against Cloud Storage. Try a pipeline that keeps and fetches an artifact before relying on it.

Create the bucket and a service account with the Storage Object User role on it, then an HMAC key
for that service account. Put the key in a Secret under the names the store reads:

```sh
gcloud storage hmac create launchway-artifacts@acme-ci.iam.gserviceaccount.com
kubectl -n launchway create secret generic launchway-gcs \
  --from-literal=AWS_ACCESS_KEY_ID=GOOG1E... --from-literal=AWS_SECRET_ACCESS_KEY=...
```

The store reads the key once, at start. It does not use GKE Workload Identity. Rotate the HMAC key
by updating the Secret and restarting the servers.

## Sign-in

Register Launchway with your OpenID Connect provider as a confidential web application with the
redirect URI `https://launchway.example.com/auth/callback`, and keep its client secret in a Secret:

```sh
kubectl -n launchway create secret generic launchway-oidc --from-literal=client-secret=...
```

## Step pods on their own node pool

On GKE Standard, give the builds a node pool of their own, called `builds` here; GKE labels its
nodes `cloud.google.com/gke-nodepool=builds`. Create the step namespace with Pod Security
`restricted`:

```sh
kubectl create namespace launchway-steps
kubectl label namespace launchway-steps pod-security.kubernetes.io/enforce=restricted
```

Step pods take no tolerations, so do not taint the builds node pool. On Autopilot, leave
`runner.kubernetes.nodeSelector` out. Step pods get no Google credentials from the chart (no
ServiceAccount token is mounted into them); with Workload Identity Federation for GKE turned on,
they also cannot use the node's service account through the metadata server.

## The Gateway

The values file attaches an HTTPRoute to a Gateway you run, here `internal-https` in namespace
`gateway-system`, with an HTTPS listener for `launchway.example.com`. GKE's load balancer checks
backend health and times out requests after 30 seconds by default; the UI's live updates
(`/api/v1/stream`) need longer. Give the Service a health check on `/readyz` and a longer timeout
with GKE's policies:

```yaml
apiVersion: networking.gke.io/v1
kind: HealthCheckPolicy
metadata:
  name: launchway
  namespace: launchway
spec:
  default:
    config:
      type: HTTP
      httpHealthCheck:
        port: 8080
        requestPath: /readyz
  targetRef:
    group: ""
    kind: Service
    name: launchway
---
apiVersion: networking.gke.io/v1
kind: GCPBackendPolicy
metadata:
  name: launchway
  namespace: launchway
spec:
  default:
    timeoutSec: 3600
  targetRef:
    group: ""
    kind: Service
    name: launchway
```

## Secrets from Vault

Runners log in to Vault or OpenBao with Kubernetes auth, using their own ServiceAccount token
(`launchway-runner` in the release namespace). Configure Vault's Kubernetes auth method for the GKE
cluster: its API server address, its CA certificate, and a role bound to that ServiceAccount in
namespace `launchway`. This is Vault's own Kubernetes auth; Launchway does not use Google
identities for it. If Vault checks tokens with the client's own token rather than a reviewer token,
the runners' ServiceAccount also needs the `system:auth-delegator` ClusterRole. See
[Secrets](secrets.md).

## The values file

This is `deploy/helm/launchway/examples/gke.yaml`:

```yaml
# Launchway on Google Kubernetes Engine: three servers across zones, Cloud SQL for PostgreSQL over
# a private IP, artifacts in Cloud Storage through its S3-compatible XML API, the UI behind a GKE
# Gateway, and step pods on a node pool of their own. See docs/howto/install-gke.md for the steps
# around it.
#
#   kubectl create namespace launchway
#   kubectl create namespace launchway-steps
#   kubectl label namespace launchway-steps pod-security.kubernetes.io/enforce=restricted
#   kubectl -n launchway create secret generic launchway-database \
#     --from-literal=url='postgres://launchway:PASSWORD@10.20.0.3:5432/launchway?sslmode=verify-ca&sslrootcert=/etc/launchway-db-ca/server-ca.pem'
#   kubectl -n launchway create configmap launchway-db-ca --from-file=server-ca.pem
#   kubectl -n launchway create secret generic launchway-gcs \
#     --from-literal=AWS_ACCESS_KEY_ID=GOOG1E... --from-literal=AWS_SECRET_ACCESS_KEY=...
#   kubectl -n launchway create secret generic launchway-oidc --from-literal=client-secret=...
#   helm install launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/gke.yaml

image:
  # Your copy of the image, in Artifact Registry. Pin a digest in production; it wins over the tag.
  repository: europe-west1-docker.pkg.dev/acme-ci/launchway/launchway
  tag: "0.1.0"

database:
  existingSecret: launchway-database
  existingSecretKey: url

config:
  projects:
    - name: payments
      repo: https://git.example.com/team/payments.git
      branch: main
      poll: 30s
  # Artifacts and SBOMs in one Cloud Storage bucket, through the XML API with an HMAC key.
  # Only the servers talk to it.
  artifacts:
    store: s3
    s3:
      endpoint: https://storage.googleapis.com
      region: auto
      bucket: acme-launchway-artifacts
      prefix: launchway/
      existingSecret: launchway-gcs
  extraConfig:
    server:
      trust_proxy: true          # every request arrives through the Gateway
    auth:
      mode: oidc
      oidc:
        issuer: https://sso.example.com/realms/acme
        client_id: launchway
        client_secret_env: LAUNCHWAY_OIDC_SECRET
        redirect_url: https://launchway.example.com/auth/callback
      access:
        - { group: platform-admins, role: admin }
        - { group: payments, role: operator, projects: [payments] }
        - { group: everyone, role: viewer }

server:
  replicas: 3
  extraEnv:
    - name: LAUNCHWAY_OIDC_SECRET
      valueFrom:
        secretKeyRef: { name: launchway-oidc, key: client-secret }
  # The Cloud SQL server CA named by sslrootcert in the database URL.
  extraVolumes:
    - name: db-ca
      configMap: { name: launchway-db-ca }
  extraVolumeMounts:
    - { name: db-ca, mountPath: /etc/launchway-db-ca, readOnly: true }
  topologySpreadConstraints:
    - maxSkew: 1
      topologyKey: topology.kubernetes.io/zone
      whenUnsatisfiable: ScheduleAnyway
      labelSelector:
        matchLabels: { app.kubernetes.io/name: launchway, app.kubernetes.io/instance: launchway, app.kubernetes.io/component: server }
      matchLabelKeys: [pod-template-hash]

# A Gateway you run (see the guide), with an HTTPS listener for launchway.example.com.
httpRoute:
  enabled: true
  parentRefs:
    - name: internal-https
      namespace: gateway-system
  hostnames: [launchway.example.com]

runner:
  replicas: 3
  concurrency: 4
  kubernetes:
    namespace: launchway-steps
    # Step pods go to the node pool for builds. Step pods take no tolerations, so do not taint it.
    nodeSelector: { cloud.google.com/gke-nodepool: builds }
    cpu: 250m
    memory: 256Mi
    memoryLimit: 4Gi
  dataVolume:
    sizeLimit: 20Gi
  # Vault with Kubernetes auth, logging in with the runner pods' own service account token.
  secretStores:
    vault:
      type: vault
      address: https://vault.example.com:8200
      role: launchway-runner
      keys: ["ci/*"]
```

What the parts do:

- `artifacts.s3` points at Cloud Storage's XML API with path-style requests (the default) and
  `region: auto`. `prefix` keeps Launchway's objects under `launchway/`.
- `server.trust_proxy: true` makes the servers take the client address and scheme from the
  Gateway, which every request passes through.
- `httpRoute` attaches the Service to your Gateway. Use an internal Gateway class unless sign-in is
  on and you have read [Hardening](../security/hardening.md).
- `topologySpreadConstraints` spreads servers across zones first; it is soft, so a zone outage does
  not block scheduling.

## Install

```sh
helm install launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/gke.yaml
kubectl apply -f launchway-gateway-policies.yaml
kubectl -n launchway get pods
kubectl -n launchway get httproute launchway
```

`launchway-gateway-policies.yaml` holds the two policies above. Point `launchway.example.com` at the
Gateway's address, open `https://launchway.example.com/` and sign in. Check the servers and runners:

```sh
kubectl -n launchway logs deploy/launchway-server | grep -e 'store=s3' -e 'listening'
kubectl -n launchway get pods -l app.kubernetes.io/component=runner
```

A runner is ready only while the servers answer it, so ready runners mean they reached the servers.

## Check it before installing

To see exactly what the chart will create, render the values file and lint it:

```sh
helm lint deploy/helm/launchway -f deploy/helm/launchway/examples/gke.yaml
helm template launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/gke.yaml
```

## What next

- [Connect a runner](connect-runner.md) in another project or network.
- [Back up, restore and upgrade](backup-restore-upgrade.md).
- [Install on Kubernetes](install-kubernetes.md) for scaling, the deploy runner and upgrades.
- [Helm values](../reference/helm-values.md) for every setting.
