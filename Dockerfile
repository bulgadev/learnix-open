# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go sqlite driver (modernc.org/sqlite) + static binary → no CGO needed.
RUN CGO_ENABLED=0 go build -o /out/learnix .
# Data dir owned by the runtime user; an empty named volume inherits this
# ownership on first mount.
RUN mkdir -p /out/data && chown -R 1000:1000 /out/data

# Runtime stage: minimal static image, non-root
FROM scratch
ARG LEARNIX_BUILD_VERSION=dev
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/learnix /learnix
COPY --from=builder --chown=1000:1000 /out/data /data
COPY --chown=1000:1000 static /static
ENV PORT=8080 \
    DB_PATH=/data/learnix.db \
    LEARNIX_BUILD_VERSION=$LEARNIX_BUILD_VERSION
WORKDIR /
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/learnix"]
CMD ["-port", "8080", "-db", "/data/learnix.db"]
