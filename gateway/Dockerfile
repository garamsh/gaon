# Builds gaon-gateway-proxy from this module and the module it replaces locally.
# The context is gateway/ and the named context libapi is lib/api/, because
# go.mod replaces lib/api with ../lib/api:
#   docker build --build-context libapi=lib/api -f gateway/Dockerfile gateway
# Both base images are pinned by digest. Dependabot proposes the bump of each
# digest: the distroless one only within the nonroot tag, the golang one only
# within the minor of the tag below (.github/dependabot.yml ignores minor and
# major golang updates). The owner of this file moves the golang minor, together
# with the toolchain directive in go.work, which the tag follows.
FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS build
WORKDIR /src/gateway
# go.work names every module of the repository; this build sees only this one
# and the one its replace directive points at.
ENV GOWORK=off GOFLAGS=-mod=readonly
COPY --from=libapi go.mod go.sum /src/lib/api/
COPY go.mod go.sum ./
RUN go mod download
COPY --from=libapi . /src/lib/api
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/gaon-gateway-proxy ./cmd/gaon-gateway-proxy

# static-debian13 holds no shell and no init, and its nonroot tag runs as uid
# 65532.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /out/gaon-gateway-proxy /gaon-gateway-proxy
USER 65532:65532
ARG IMAGE_REVISION=
ARG IMAGE_VERSION=
LABEL org.opencontainers.image.source="https://github.com/garamsh/gaon" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.version="${IMAGE_VERSION}"
ENTRYPOINT ["/gaon-gateway-proxy"]
