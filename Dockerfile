FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=notarx-go-mod,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY . .
RUN --mount=type=cache,id=notarx-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=notarx-go-build,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$VERSION -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$REVISION" \
    -o /notarx ./cmd/notarx
RUN --mount=type=cache,id=notarx-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=notarx-go-build,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$VERSION -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$REVISION" \
    -o /notarx-healthcheck ./cmd/healthcheck

# Opt-in test stage: `docker build --target test .` (make docker-test). The image
# build does not depend on it, so BuildKit skips it; CI runs the suite once in
# the contract and quality jobs instead of once per image build.
FROM build AS test
RUN --mount=type=cache,id=notarx-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=notarx-go-build,target=/root/.cache/go-build,sharing=locked \
    go test ./...

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE=https://github.com/mlops-ca/notarx
LABEL org.opencontainers.image.title="notarx" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.source="$SOURCE"
COPY --from=build /notarx /notarx
COPY --from=build /notarx-healthcheck /notarx-healthcheck
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/notarx"]
