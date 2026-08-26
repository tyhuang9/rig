# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS build

ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    GOTOOLCHAIN=local GOFLAGS=-mod=readonly go mod download

COPY cmd/rig-relay ./cmd/rig-relay
COPY cmd/rig-relay-probe ./cmd/rig-relay-probe
COPY internal/relay ./internal/relay

RUN test "$TARGETOS" = linux && test -n "$TARGETARCH" && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOTOOLCHAIN=local \
    go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /out/rig-relay ./cmd/rig-relay && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOTOOLCHAIN=local \
    go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /out/rig-relay-probe ./cmd/rig-relay-probe

FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="Rig webhook relay" \
      org.opencontainers.image.description="Outbound-controller GitHub webhook relay" \
      org.opencontainers.image.source="https://github.com/hostd/hostd" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.created="$CREATED"

COPY --from=build --chmod=0555 /out/rig-relay /usr/local/bin/rig-relay
COPY --from=build --chmod=0555 /out/rig-relay-probe /usr/local/bin/rig-relay-probe

USER 65532:65532
EXPOSE 7346
ENTRYPOINT ["/usr/local/bin/rig-relay"]
