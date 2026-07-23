# syntax=docker/dockerfile:1.7

# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary. modernc.org/sqlite is pure Go, so CGO stays off and the
# result runs on the distroless "static" image.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/wavonyx \
    ./cmd/wavonyx

# Empty data dir used to seed the named volume with nonroot ownership.
RUN mkdir -p /data

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wavonyx /usr/local/bin/wavonyx
COPY --from=build --chown=65532:65532 /data /data

ENV WAVONYX_ADDR=":9900" \
    WAVONYX_DATA_DIR="/data"

EXPOSE 9900
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/wavonyx", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/wavonyx", "serve"]
