# The base images, by digest (their multi-arch index): a tag published again
# does not change what builds. The Go toolchain is go.mod's; Alpine a
# supported branch (https://alpinelinux.org/releases/). Bumped by hand, the
# tag and its digest together (docker buildx imagetools inspect <image>).
ARG GO_IMAGE=golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b
ARG ALPINE_IMAGE=alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

# Cross-compile on the native build platform (no QEMU emulation): the Go
# toolchain runs natively and emits a static binary for the target arch.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-build

ARG TARGETOS=linux
ARG TARGETARCH
# Version of okdp-gitops (no leading "v"), named in the co-author trailer of
# the commits it writes. CI passes the release version.
ARG VERSION=dev

WORKDIR /workspace/okdp-gitops

# okdp-compiler and cel2yaml are modules of go.mod like any other,
# downloaded at the releases it requires.
COPY go.* ./
RUN go mod download

COPY catalog/ catalog/
COPY chartcache/ chartcache/
COPY cmd/ cmd/
COPY deploy/ deploy/
COPY gitops/ gitops/
COPY internal/ internal/
COPY rules/ rules/
COPY treeedit/ treeedit/

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X github.com/okdp/okdp-gitops/internal/buildinfo.Version=${VERSION}" \
      -o /okdp-gitops ./cmd/okdp-gitops

# Alpine, not scratch: the jobs of the CI of the output run in this image (the
# runner maps its okdp-gitops label to it), their script with its shell (sh,
# mktemp, cat), as the user 65534; okdp-gitops needs no git binary (go-git).
FROM ${ALPINE_IMAGE}

RUN apk --no-cache add ca-certificates && update-ca-certificates

COPY --from=go-build /okdp-gitops /usr/local/bin/okdp-gitops

# Writable HOME for the unprivileged user (Git and registry clients).
ENV HOME=/tmp

USER 65534:65534

ENTRYPOINT ["okdp-gitops"]
