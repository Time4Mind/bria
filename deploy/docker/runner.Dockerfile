FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bria ./cmd/bria

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tmux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 bria-agent \
    && useradd --uid 10001 --gid 10001 --home-dir /srv/bria-agent --create-home bria-agent
COPY --from=build /out/bria /usr/local/bin/bria
USER 10001:10001
WORKDIR /srv/bria-agent
ENTRYPOINT ["/usr/local/bin/bria"]
