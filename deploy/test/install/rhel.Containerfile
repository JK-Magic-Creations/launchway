# A RHEL-compatible machine with systemd, for make test-install. BASE is pinned by digest in
# test.sh.
ARG BASE
FROM ${BASE}
RUN dnf install -q -y systemd git-core openssl shadow-utils \
    && dnf clean all \
    && systemctl mask systemd-logind.service getty.target console-getty.service >/dev/null 2>&1 || true
STOPSIGNAL SIGRTMIN+3
CMD ["/usr/sbin/init"]
