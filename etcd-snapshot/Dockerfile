# Builds gaon-etcd-snapshot from this module alone; the context is etcd-snapshot/.
# Both base images are pinned by digest. Their mover is a manual bump by the
# owner of this file: the golang tag follows the toolchain directive in go.work,
# and the distroless digest is re-read when its tag is rebuilt.
FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build
WORKDIR /src
# go.work names every module of the repository; this build sees only this one.
ENV GOWORK=off GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# The credential helper's PKCS#11 signer is cgo only, so the build needs cgo and
# links it statically; netgo and osusergo keep the resolver and user lookups
# off glibc's NSS, which a static glibc binary cannot load.
RUN CGO_ENABLED=1 go build -trimpath -tags netgo,osusergo -ldflags='-s -w -linkmode external -extldflags "-static"' -o /out/gaon-etcd-snapshot ./cmd/gaon-etcd-snapshot

# static-debian13 holds the CA bundle S3 and Roles Anywhere TLS verify against,
# no shell and no init, and its nonroot tag runs as uid 65532.
FROM gcr.io/distroless/static-debian13@sha256:1a64f46364620465ef9e9a8f5413757dfdf22695e050bbfd7303eeaf17a59383
COPY --from=build /out/gaon-etcd-snapshot /gaon-etcd-snapshot
USER 65532:65532
ARG IMAGE_REVISION=
ARG IMAGE_VERSION=
LABEL org.opencontainers.image.source="https://github.com/garamsh/gaon" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.version="${IMAGE_VERSION}"
ENTRYPOINT ["/gaon-etcd-snapshot"]
