# syntax=docker/dockerfile:1
# Docker Hub official-images multi-arch OCI indexes, resolved 2026-09-03.
ARG GO_IMAGE=golang:1.22.12-alpine3.21@sha256:1699c10032ca2582ec89a24a1312d986a3f094aed3d5c1147b19880afe40e052
ARG RUNTIME_IMAGE=alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

FROM ${GO_IMAGE} AS build
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN case "$VERSION" in ""|dev|.*|*..*|*[!0-9A-Za-z._+-]*) exit 2 ;; esac && \
    case "$REVISION" in ""|unknown|*[!0-9A-Za-z._+-]*) exit 2 ;; esac && \
    CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=$VERSION" -o /out/bria ./cmd/bria && \
    CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-s -w" -o /out/bria-codex-adapter ./cmd/bria-codex-adapter && \
    CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-s -w" -o /out/bria-claude-adapter ./cmd/bria-claude-adapter
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-s -w" -o /out/bria-container-preflight ./cmd/bria-container-preflight

FROM ${RUNTIME_IMAGE}
ARG VERSION
ARG REVISION
LABEL org.opencontainers.image.title="Bria" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      io.time4mind.bria.role-selection="config-and-fail-closed-preflight"
RUN addgroup -S -g 65532 bria && adduser -S -D -H -u 65532 -G bria bria
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/bria /out/bria-codex-adapter /out/bria-claude-adapter /out/bria-container-preflight /opt/bria/
COPY docker/entrypoint.sh /opt/bria/entrypoint.sh
RUN chmod 0555 /opt/bria/bria /opt/bria/bria-codex-adapter \
      /opt/bria/bria-claude-adapter /opt/bria/bria-container-preflight /opt/bria/entrypoint.sh && \
    mkdir -p /var/lib/bria/provider-auth /workspaces /opt/bria/providers /opt/bria/models && \
    chown -R 65532:65532 /var/lib/bria /workspaces && \
    chmod 0555 /opt/bria/providers /opt/bria/models

USER 65532:65532
WORKDIR /var/lib/bria
ENV BRIA_CONFIG=/etc/bria/config.json
ENV BRIA_PROVIDER_LOCK=/etc/bria/provider-lock.json
ENV BRIA_ROLE=combined
ENV HOME=/var/lib/bria/provider-auth
ENTRYPOINT ["/opt/bria/entrypoint.sh"]
CMD []
