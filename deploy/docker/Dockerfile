# syntax=docker/dockerfile:1.7
# sb-analysis's images:
#   default (or --target sb-analysis): sb-analysis and sb-eval, distroless
#   --target analysis-media: the same binaries with ffmpeg, for the media
#     workers (video key frames and audio for speech-to-text)
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/ ./cmd/sb-analysis ./cmd/sb-eval

FROM debian:12-slim AS analysis-media
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 65532 --create-home --home-dir /home/nonroot nonroot
COPY --from=build /out/ /usr/local/bin/
ENV HOME=/tmp
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/sb-analysis"]

FROM gcr.io/distroless/static-debian12:nonroot AS sb-analysis
COPY --from=build /out/ /usr/local/bin/
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/sb-analysis"]
