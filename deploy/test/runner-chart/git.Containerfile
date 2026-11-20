# A Git server for make test-runner-chart: git daemon serving one repository made at start from
# the pipeline in a ConfigMap. BASE is Alpine, pinned by digest in test.sh.
ARG BASE
FROM ${BASE}
RUN apk add --no-cache git git-daemon
USER 65532:65532
