FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=artifact-gateway-go-mod,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY . .
RUN --mount=type=cache,id=artifact-gateway-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=artifact-gateway-go-build,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$VERSION -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$REVISION" \
    -o /notarx ./cmd/notarx
RUN --mount=type=cache,id=artifact-gateway-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=artifact-gateway-go-build,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$VERSION -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$REVISION" \
    -o /gateway-healthcheck ./cmd/healthcheck

# Opt-in test stage: `docker build --target test .` (make docker-test). The image
# build does not depend on it, so BuildKit skips it; CI runs the suite once in
# the contract and quality jobs instead of once per image build.
FROM build AS test
RUN --mount=type=cache,id=artifact-gateway-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=artifact-gateway-go-build,target=/root/.cache/go-build,sharing=locked \
    go test ./...

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE=https://github.com/mlops-ca/notarx
LABEL org.opencontainers.image.title="Artifact Gateway" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.source="$SOURCE"
COPY --from=build /notarx /notarx
COPY --from=build /gateway-healthcheck /gateway-healthcheck
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/notarx"]
