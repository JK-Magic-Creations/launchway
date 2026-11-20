# The Launchway image: one static binary (server and runner), Git, and CA
# certificates. Build it with `make image`, or:
#
#   podman build --build-arg VERSION=1.2.3 --build-arg REVISION=$(git rev-parse HEAD) \
#     --build-arg CREATED=$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ) \
#     -t launchway:1.2.3 .
#
# REVISION (the full commit) is required: the build stops without it, so no image is labelled
# with an unknown revision.
#
# The final image holds the binary, Git (with its HTTPS and SSH transports)
# and CA certificates, and nothing else: no package manager and no shell.
# Git needs a shell only for settings that are shell commands, such as
# GIT_SSH_COMMAND or a credential helper starting with "!"; use GIT_SSH or
# ~/.ssh/config instead. To debug a running pod, use `kubectl debug`.

ARG NODE_IMAGE=docker.io/library/node:24.22.3-alpine3.23
ARG GO_IMAGE=docker.io/library/golang:1.27.4-alpine3.23
ARG ALPINE_IMAGE=docker.io/library/alpine:3.23.8

# ---- the web UI ----
FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- the binary ----
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=web /src/web/dist/ web/dist/
ARG VERSION=dev
ARG REVISION=
ARG CREATED=
RUN echo "$REVISION" | grep -Eq '^[0-9a-f]{40}([0-9a-f]{24})?$' \
    || { echo "REVISION must be the full commit (make image sets it): got '$REVISION'" >&2; exit 1; } \
    && echo "$CREATED" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' \
    || { echo "CREATED must be the commit's time in UTC, such as 2026-11-20T06:12:56Z: got '$CREATED'" >&2; exit 1; }
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/launchway ./cmd/launchway

# ---- the runtime file system ----
# Packages are installed into an empty root, without their install scripts,
# and the programs Git does not use are removed. The package database is kept
# so that image scanners can see what is installed.
FROM ${ALPINE_IMAGE} AS rootfs
RUN apk add --root /rootfs --initdb --no-cache --no-scripts \
        --keys-dir /etc/apk/keys --repositories-file /etc/apk/repositories \
        ca-certificates-bundle git openssh-client-default \
    && rm -f /rootfs/usr/bin/scp /rootfs/usr/bin/sftp /rootfs/usr/bin/ssh-add /rootfs/usr/bin/ssh-agent \
        /rootfs/usr/bin/ssh-copy-id /rootfs/usr/bin/ssh-keyscan /rootfs/usr/bin/ssh-pkcs11-helper \
        /rootfs/usr/bin/findssl.sh /rootfs/usr/bin/git-shell \
    && rm -rf /rootfs/etc/apk /rootfs/var/cache/apk \
    && mkdir -p /rootfs/tmp /rootfs/home/launchway /rootfs/var/lib/launchway /rootfs/etc/launchway \
    && chmod 1777 /rootfs/tmp \
    && printf 'root:x:0:0:root:/root:/sbin/nologin\nlaunchway:x:65532:65532:launchway:/home/launchway:/sbin/nologin\n' > /rootfs/etc/passwd \
    && printf 'root:x:0:\nlaunchway:x:65532:\n' > /rootfs/etc/group \
    && chown 65532:65532 /rootfs/home/launchway /rootfs/var/lib/launchway

FROM scratch
ARG VERSION=dev
ARG REVISION
ARG CREATED
ARG SOURCE=https://github.com/JK-Magic-Creations/launchway
LABEL org.opencontainers.image.title="Launchway" \
      org.opencontainers.image.description="A build and delivery platform: server, web UI and runner in one binary" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.url="${SOURCE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
COPY --from=rootfs /rootfs/ /
COPY --from=build /out/launchway /usr/local/bin/launchway
ENV HOME=/home/launchway \
    PATH=/usr/local/bin:/usr/bin:/bin
USER 65532:65532
WORKDIR /var/lib/launchway
EXPOSE 8080
ENTRYPOINT ["launchway"]
CMD ["server", "--config", "/etc/launchway/launchway.yaml"]
