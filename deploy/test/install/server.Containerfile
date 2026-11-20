# The Launchway server the installed runners connect to in make test-install: the release
# binary (mounted at run time), Git, and git daemon serving the test repository.
ARG BASE
FROM ${BASE}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -qq && apt-get install -qq -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
