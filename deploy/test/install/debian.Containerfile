# A Debian or Ubuntu machine with systemd, for make test-install. BASE is the distribution's
# image, pinned by digest in test.sh.
ARG BASE
FROM ${BASE}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -qq \
    && apt-get install -qq -y --no-install-recommends systemd systemd-sysv dbus curl ca-certificates git openssl passwd \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask systemd-logind.service getty.target console-getty.service >/dev/null 2>&1 || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
