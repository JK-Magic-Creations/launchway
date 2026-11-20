# Install Launchway on Amazon EKS

This guide runs Launchway on Amazon EKS with the Helm chart: servers spread across availability
zones, Amazon RDS or Aurora PostgreSQL as the shared database, artifacts in Amazon S3, the UI behind
an Application Load Balancer, and step pods on a node group of their own. It is for operators who
have read [Install on Kubernetes](install-kubernetes.md), which explains the chart itself.

> **Verified** on 2026-11-19 against commit f717337: `helm lint` and `helm template` of
> `deploy/helm/launchway/examples/eks.yaml` (the values file below), and `helm install` of the same
> file on a local kind cluster with only what cannot exist there replaced (the image, the database
> URL, the S3 endpoint pointed at MinIO, the step namespace, no node selector): three servers and
> three runners became ready, the servers stored artifacts in the bucket under the prefix and a
> pipeline fetched them back, and the API refused unauthenticated requests with sign-in on.
> **Not verified:** nothing was run on AWS. Not tested: EKS itself, RDS or Aurora (including
> `sslmode=verify-full` with the RDS certificate bundle), Amazon S3 itself, the ALB and its
> annotations, ECR, node groups, and Vault Kubernetes auth from EKS.

## What you need

- An EKS cluster on Kubernetes 1.30 or later, `kubectl` for it, and Helm 3.8 or later.
- The [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/)
  installed in the cluster, and an ACM certificate for the name people will use, here
  `launchway.example.com`.
- An Amazon ECR repository for the Launchway image, and Podman or Docker to build it.
- An RDS for PostgreSQL instance or Aurora PostgreSQL cluster that the cluster's nodes can reach.
- An S3 bucket for artifacts, and an IAM user with an access key that may use it.
- An OpenID Connect provider for sign-in (see [Sign-in with OpenID Connect and roles](oidc-and-roles.md)).
- Optional: a managed node group for step pods, and Vault or OpenBao for secrets.

## The layout

```mermaid
flowchart LR
  users["People"] --> alb["ALB (internal, HTTPS)"]
  alb --> servers["Servers, 3 across zones"]
  runners["Runners"] --> servers
  servers --> rds[("RDS or Aurora PostgreSQL")]
  servers --> s3[("S3 bucket")]
  runners --> eksapi["EKS API"]
  eksapi --> steps["Step pods on the builds node group"]
  runners --> vault["Vault"]
```

Only the servers use the database and the bucket. Runners talk to the servers, the Kubernetes API,
your Git host and your secret stores, all outbound.

## Build and push the image

Once releases are published you can mirror the signed release image
`ghcr.io/jk-magic-creations/launchway:VERSION` into ECR instead (check it first as
[Verifying downloads](../security/verify-downloads.md#check-an-image) describes). Until then:

Build the image from a clone of the repository and push it to ECR:

```sh
make image IMAGE=localhost/launchway:0.1.0
aws ecr get-login-password --region eu-west-1 | \
  podman login --username AWS --password-stdin 123456789012.dkr.ecr.eu-west-1.amazonaws.com
podman tag localhost/launchway:0.1.0 123456789012.dkr.ecr.eu-west-1.amazonaws.com/launchway:0.1.0
podman push 123456789012.dkr.ecr.eu-west-1.amazonaws.com/launchway:0.1.0
```

EKS nodes pull from ECR in the same account with their node role. Pin the image by digest in
production with `image.digest`.

## The database

Create a database and a user for Launchway on your RDS instance or Aurora cluster. The user must be
able to create tables in that database: servers apply schema migrations when they start. All
servers use the same database and agree through it, so there is nothing else to set up for high
availability. With Aurora, use the cluster (writer) endpoint, not a reader endpoint.

Verify the server's certificate. Download the RDS certificate bundle (`global-bundle.pem`, from the
Amazon RDS documentation on SSL/TLS) and give it to the servers in a ConfigMap; the database URL names
it with `sslrootcert`:

```sh
kubectl create namespace launchway
kubectl -n launchway create configmap launchway-db-ca --from-file=global-bundle.pem
kubectl -n launchway create secret generic launchway-database \
  --from-literal=url='postgres://launchway:PASSWORD@launchway.cluster-abc123.eu-west-1.rds.amazonaws.com:5432/launchway?sslmode=verify-full&sslrootcert=/etc/launchway-db-ca/global-bundle.pem'
```

The values file mounts that ConfigMap at `/etc/launchway-db-ca` in the server pods. Back the
database up with RDS snapshots and point-in-time recovery; see
[Back up, restore and upgrade](backup-restore-upgrade.md).

## Artifact storage in S3

The servers keep artifacts and SBOMs in an S3-compatible store, signed with AWS Signature Version 4.
Create a bucket, and an IAM user whose policy allows `s3:PutObject`, `s3:GetObject`,
`s3:DeleteObject` and `s3:AbortMultipartUpload` on
`arn:aws:s3:::acme-launchway-artifacts/launchway/*`. Put its access key in a Secret:

```sh
kubectl -n launchway create secret generic launchway-s3 \
  --from-literal=AWS_ACCESS_KEY_ID=AKIA... --from-literal=AWS_SECRET_ACCESS_KEY=...
```

> [!NOTE]
> The S3 store reads a static access key (and optionally `AWS_SESSION_TOKEN`) from the environment
> once, at start. It does not use IAM roles for service accounts or EKS Pod Identity, and it does not
> refresh temporary credentials. Use an IAM user's access key and rotate it by updating the Secret
> and restarting the servers.

The bucket must exist before the servers start: each server writes a small marker object under the
prefix when it starts, and logs `artifacts are kept in a store store=s3`.

## Sign-in

Register Launchway with your OpenID Connect provider as a confidential web application with the
redirect URI `https://launchway.example.com/auth/callback`, and keep its client secret in a Secret:

```sh
kubectl -n launchway create secret generic launchway-oidc --from-literal=client-secret=...
```

## Step pods on their own node group

Give the builds a managed node group of their own, with a Kubernetes label such as
`launchway.example.com/pool=builds`, and create the step namespace with Pod Security `restricted`:

```sh
kubectl create namespace launchway-steps
kubectl label namespace launchway-steps pod-security.kubernetes.io/enforce=restricted
```

Step pods are scheduled with `runner.kubernetes.nodeSelector`. They take no tolerations, so do not
taint the builds node group; to keep other workloads off it, give those workloads a node selector
or affinity for their own node groups instead. Step pods get no AWS credentials: the chart mounts no
ServiceAccount token into them, and they should not be able to reach the instance metadata service
for the node role (require IMDSv2 with a hop limit of 1 on the builds node group).

## Secrets from Vault

Runners log in to Vault or OpenBao with Kubernetes auth, using their own ServiceAccount token
(`launchway-runner` in the release namespace). Configure Vault's Kubernetes auth method for the EKS
cluster: its API server address, its CA certificate, and a role bound to the ServiceAccount
`launchway-runner` in namespace `launchway`. This is Vault's own Kubernetes auth, not an AWS
identity; Launchway does not use IAM roles for it. If Vault checks tokens with the client's own
token rather than a reviewer token, the runners' ServiceAccount also needs the `system:auth-delegator`
ClusterRole. See [Secrets](secrets.md).

## The values file

This is `deploy/helm/launchway/examples/eks.yaml`:

```yaml
# Launchway on Amazon EKS: three servers across zones, Amazon RDS or Aurora PostgreSQL, artifacts
# in Amazon S3, the UI behind an AWS Load Balancer Controller ALB, and step pods on a node group
# of their own. See docs/howto/install-eks.md for the steps around it.
#
#   kubectl create namespace launchway
#   kubectl create namespace launchway-steps
#   kubectl label namespace launchway-steps pod-security.kubernetes.io/enforce=restricted
#   kubectl -n launchway create secret generic launchway-database \
#     --from-literal=url='postgres://launchway:PASSWORD@launchway.cluster-abc123.eu-west-1.rds.amazonaws.com:5432/launchway?sslmode=verify-full&sslrootcert=/etc/launchway-db-ca/global-bundle.pem'
#   kubectl -n launchway create configmap launchway-db-ca --from-file=global-bundle.pem
#   kubectl -n launchway create secret generic launchway-s3 \
#     --from-literal=AWS_ACCESS_KEY_ID=AKIA... --from-literal=AWS_SECRET_ACCESS_KEY=...
#   kubectl -n launchway create secret generic launchway-oidc --from-literal=client-secret=...
#   helm install launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/eks.yaml

image:
  # Your copy of the image, in Amazon ECR. Pin a digest in production; it wins over the tag.
  repository: 123456789012.dkr.ecr.eu-west-1.amazonaws.com/launchway
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
  # Artifacts and SBOMs in one bucket that every server shares. Only the servers talk to it.
  artifacts:
    store: s3
    s3:
      endpoint: https://s3.eu-west-1.amazonaws.com
      region: eu-west-1
      bucket: acme-launchway-artifacts
      prefix: launchway/
      existingSecret: launchway-s3
  extraConfig:
    server:
      trust_proxy: true          # every request arrives through the ALB
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
  # The RDS certificate bundle named by sslrootcert in the database URL.
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

ingress:
  enabled: true
  className: alb
  annotations:
    alb.ingress.kubernetes.io/scheme: internal
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS": 443}]'
    alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000
    alb.ingress.kubernetes.io/healthcheck-path: /readyz
    # The UI keeps /api/v1/stream open for live updates.
    alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
  hosts: [launchway.example.com]

runner:
  replicas: 3
  concurrency: 4
  kubernetes:
    namespace: launchway-steps
    # Step pods go to the node group for builds, by a label its nodes carry. Step pods take no
    # tolerations, so do not taint that node group.
    nodeSelector: { launchway.example.com/pool: builds }
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

- `artifacts.s3` uses the regional endpoint with path-style requests (the default), which S3
  accepts. `prefix` keeps Launchway's objects under `launchway/`.
- `server.trust_proxy: true` makes the servers take the client address and scheme from the ALB,
  which every request passes through.
- The ALB health check uses `/readyz`, which reports not ready as soon as a server starts stopping.
  The idle timeout of 3600 seconds keeps the UI's live updates (`/api/v1/stream`) open.
- `scheme: internal` keeps the ALB inside your VPC. Change it to `internet-facing` only with sign-in
  on and after reading [Hardening](../security/hardening.md).
- `topologySpreadConstraints` spreads servers across zones first; it is soft, so a zone outage does
  not block scheduling.

## Install

```sh
helm install launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/eks.yaml
kubectl -n launchway get pods
kubectl -n launchway get ingress launchway
```

Point `launchway.example.com` at the ALB's address (a CNAME, or an alias record in Route 53). Then
open `https://launchway.example.com/` and sign in. Check the servers and runners:

```sh
kubectl -n launchway logs deploy/launchway-server | grep -e 'store=s3' -e 'listening'
kubectl -n launchway get pods -l app.kubernetes.io/component=runner
```

A runner is ready only while the servers answer it, so ready runners mean they reached the servers.

## Check it before installing

To see exactly what the chart will create, render the values file and lint it:

```sh
helm lint deploy/helm/launchway -f deploy/helm/launchway/examples/eks.yaml
helm template launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/eks.yaml
```

## What next

- [Connect a runner](connect-runner.md) in another VPC or account.
- [Back up, restore and upgrade](backup-restore-upgrade.md).
- [Install on Kubernetes](install-kubernetes.md) for scaling, the deploy runner and upgrades.
- [Helm values](../reference/helm-values.md) for every setting.
