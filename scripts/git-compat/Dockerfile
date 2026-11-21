# The oldest git Launchway's Git hosting supports, built from the release
# tarball, in an image that has nothing else of git: make test-git-compat runs the
# hosting tests in it. The version and the tarball's checksum (the one kernel.org
# publishes in sha256sums.asc) are build arguments; change them together with
# MinGit in internal/forge/hosting/gitcheck.go.
FROM docker.io/library/debian:bookworm-slim
ARG GIT_VERSION=2.38.0
ARG GIT_SHA256=923eade26b1814de78d06bda8e0a9f5da8b7c4b304b3f9050ffb464f0310320a
RUN apt-get update -qq \
    && apt-get install -y -qq --no-install-recommends build-essential libssl-dev zlib1g-dev libcurl4-openssl-dev gettext curl ca-certificates xz-utils \
    && curl -fsSLo /tmp/git.tar.xz https://mirrors.edge.kernel.org/pub/software/scm/git/git-${GIT_VERSION}.tar.xz \
    && echo "${GIT_SHA256}  /tmp/git.tar.xz" | sha256sum -c - \
    && tar -xJf /tmp/git.tar.xz -C /tmp \
    && make -C /tmp/git-${GIT_VERSION} -j4 prefix=/usr/local NO_TCLTK=1 NO_PERL=1 NO_PYTHON=1 NO_GETTEXT=1 NO_EXPAT=1 install >/dev/null \
    && apt-get purge -y -qq build-essential libssl-dev zlib1g-dev libcurl4-openssl-dev gettext curl xz-utils >/dev/null \
    && apt-get install -y -qq --no-install-recommends libcurl4 libssl3 zlib1g >/dev/null \
    && apt-get autoremove -y -qq >/dev/null && rm -rf /tmp/git* /var/lib/apt/lists/* \
    && git --version | grep -q "version ${GIT_VERSION}"
