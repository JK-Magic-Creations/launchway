Real outputs of the tools in tools.yaml, recorded on 2026-11-21 from their pinned images against a
small repository with a planted secret (a made-up token), a vulnerable dependency (golang.org/x/net
0.17.0, flask 0.12.2), an insecure Go and Python pattern, a bad Dockerfile, a misconfigured Kubernetes
manifest and a shell script with quoting mistakes (the repository is written by
internal/standards/standardstest). They are the input of the normalisation tests, which run everywhere.
TestRealTools runs the tools again from their images when LAUNCHWAY_STANDARDS_REAL=1 is set and a
container runtime and the network are there.
