# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.2

FROM golang:${GO_VERSION}-alpine AS base

# dev: the compose stack's hot-reload container. The tools are pinned: templ
# must match github.com/a-h/templ in go.mod, goose the one in go.mod.
FROM base AS dev
RUN go install github.com/air-verse/air@v1.67.4 && \
    go install github.com/pressly/goose/v3/cmd/goose@v3.28.0 && \
    go install github.com/a-h/templ/cmd/templ@v0.3.1020
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
CMD ["air", "-c", ".air.toml"]

# lint: `make lint` builds this stage. The module and golangci-lint caches are
# BuildKit cache mounts, so dependencies are fetched once and only changed
# packages are analyzed again. Keep the version equal to the CI action's.
FROM golangci/golangci-lint:v2.14.0 AS lint
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=cache,target=/root/.cache \
    --mount=type=bind,target=. \
    golangci-lint run

# build: one static binary from a bind-mounted source that never lands in an
# image. /out/data becomes /data, owned by the runtime user, so a fresh named
# volume is writable.
FROM --platform=$BUILDPLATFORM base AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ARG APP_VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/tikhonp/proxier/internal/platform/obs.AppVersion=${APP_VERSION}" \
    -o /out/proxier ./cmd/proxier && \
    mkdir /out/data

# prod: distroless, no shell, uid 65532. CA certificates come with the image;
# time zones are compiled into the binary (time/tzdata). Phase 1 adds nginx
# and bash for the template validators, which changes this base.
FROM gcr.io/distroless/static-debian13:nonroot AS prod
ARG APP_VERSION=dev
ARG BUILD_DATE=
ARG VCS_REF=
LABEL org.opencontainers.image.title="Proxier" \
      org.opencontainers.image.description="Self-hosted control panel for a personal proxy setup" \
      org.opencontainers.image.source="https://github.com/tikhonp/proxier" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}"

# Dozzle shows this icon for anyone who runs the image: the app's logo mark
# (a light square on the Rosé Pine base) as a base64 SVG.
LABEL dev.dozzle.icon="data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iNjAiIGhlaWdodD0iNjAiIHZpZXdCb3g9IjAgMCA2MCA2MCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj48cmVjdCB3aWR0aD0iNjAiIGhlaWdodD0iNjAiIHJ4PSI0IiBmaWxsPSIjMTkxNzI0Ii8+PHJlY3QgeD0iMTgiIHk9IjE4IiB3aWR0aD0iMjQiIGhlaWdodD0iMjQiIGZpbGw9IiNFMERFRjQiLz48L3N2Zz4="
COPY --from=build /out/proxier /bin/proxier
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
ENV PROXIER_DATA_DIR=/data
# The embedded tailnet node must not upload logs to Tailscale's servers. The
# health check is run by compose (`proxier healthcheck`), not set here.
ENV TS_NO_LOGS_NO_SUPPORT=true
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/bin/proxier"]
CMD ["serve"]
