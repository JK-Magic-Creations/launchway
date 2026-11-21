# The requirements file

`.launchway/requirements.yaml` says what the software in a repository needs: the architectures it runs on, CPU,
memory and storage, devices such as a GPU, operating-system and kernel features, network ports and data services.
Launchway reads it from the default branch into [the graph](../guide/software-graph.md), where it is used to say
which machines can run the software, what is missing, and to write a machine specification.

You do not have to write it. Launchway infers what it safely can and says so; the file is for what you know and it
does not, and for correcting what it guessed. **What is written in the file always counts; what is inferred is only
advice.** A fact the file declares is exactly what the file says: an inferred value that differs is shown as advice
and never applied.

## The file

```yaml
version: 1
platform: container          # container, vm, serverless or bootc
architectures: [amd64, arm64]
cpu: 500m                    # 2, 0.5 or 500m
memory: 512Mi                # 512Mi, 2Gi, 1G
storage: 10Gi
devices:
  - gpu                      # one GPU
  - { kind: gpu, count: 2, vendor: nvidia }
os:
  family: linux
  kernel: ">=6.1"            # a minimum
  features: [cgroups-v2]
ports: [8080, "9090/udp"]    # tcp unless it says otherwise
data:
  - postgres                 # any version
  - { kind: redis, version: ">=7" }
components:                  # what differs for one component of the repository
  worker:
    path: services/worker    # a directory, or a Containerfile
    memory: 2Gi
```

The top-level fields apply to every component of the repository. A component under `components:` overrides them
field by field. It is found by `path` (a directory, or the path of its Containerfile), or by its name.

| Field | Meaning | Values |
|---|---|---|
| `version` | the version of this file | `1` |
| `platform` | how the software is delivered | `container`, `vm`, `serverless`, `bootc` |
| `architectures` | the architectures it runs on; a machine needs one of them | `amd64`, `arm64`, `riscv64`, `ppc64le`, `s390x`, `arm`, `386` |
| `cpu` | CPU it needs | cores (`2`, `0.5`) or millicores (`500m`) |
| `memory`, `storage` | memory and storage it needs | `512Mi`, `2Gi`, `1G`, bytes |
| `devices` | devices it needs | a kind (`gpu`) or `{kind, count, vendor}` |
| `os` | operating-system needs | `family`, `kernel` (a minimum such as `>=6.1`), `features` |
| `ports` | ports it listens on | `8080`, `"8080/tcp"`, `"53/udp"` |
| `data` | data services it needs | a kind, or `{kind, version}` with `version` such as `">=15"`; kinds: `postgres`, `mysql`, `sqlite`, `mongodb`, `redis`, `memcached`, `kafka`, `rabbitmq`, `nats`, `s3`, `object-storage`, `queue`, `cache`, `database` |

Unknown fields are refused, so a misspelling is not silently ignored. A file with a mistake is refused as a whole
and each problem is listed; the graph keeps what it knew before and the problem is shown, so a bad edit never wipes
the requirements. The file may be at most 64 KiB.

## What is inferred, and from what

| Fact | Read from | Marked |
|---|---|---|
| architectures | the platforms of the image steps in `.launchway/pipeline.yaml` that build the component's Containerfile | inferred |
| platform | a Containerfile makes the component a container | inferred |
| ports | `EXPOSE` in the Containerfile, and `containerPort` in Kubernetes manifests that run the component's image | inferred |
| CPU, memory, ephemeral storage, GPUs | resource requests and limits of Kubernetes manifests in the repository (`k8s/`, `deploy/`, `manifests/`, `kubernetes/`) that run the component's image | inferred |
| runtime version | `go` in `go.mod`, `engines.node` in `package.json`, `requires-python` in `pyproject.toml` | inferred |
| data services | well-known client libraries in the manifests (a PostgreSQL driver suggests `postgres`) | inferred |

A library in a manifest does not prove the service is needed, which is why a declaration always wins and why these
are marked. Each inferred fact carries its evidence, shown on the graph page and by `launchway graph needs`.

## Changing it

From the graph page of the repository, **Edit the requirements file** opens an editor already holding what the
graph now says, and **Open a pull request** proposes it. From the command line:

```
launchway graph propose-requirements -R team/app --file requirements.yaml
```

Either way nothing changes until a person merges the pull request. The pull request is made by the server on a
branch named `launchway/requirements-…`; you need the operator role on the repository.

## The machine specification

`launchway graph spec` (and the page) adds up the requirements of components that run together into a neutral,
structured document:

| Field | Meaning |
|---|---|
| `components` | what is covered |
| `platform`, `architecture`, `architecture_options` | what they share; a conflict if they share none |
| `cpu`, `memory`, `storage` | the total required, and the total with headroom (default 25 percent) |
| `devices`, `os` | devices summed by kind; the highest kernel minimum; features together |
| `ports` | every port, and a conflict where two components want the same one |
| `data_services` | what they need that the machine, or a platform elsewhere, must provide |
| `provenance` | for each requirement, which component, declared or inferred, and the evidence |
| `conflicts`, `warnings` | what cannot be reconciled; components with no CPU or memory requirement |

It is for export. Turning it into a virtual machine definition, an image build or infrastructure code is done by the
work that builds those; nothing is provisioned from it.
