# syntax=docker/dockerfile:1
# Backend image — multi-stage Go build. CGO is disabled so the pure-Go
# modernc.org/sqlite driver is used (no C toolchain needed in the runtime
# image). See development.md §3.1/§5.1.

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a static binary.
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app
WORKDIR /data
RUN mkdir -p /data && chown -R app:app /data
USER app
COPY --from=build /out/server /server

ENV LISTEN_ADDR=:8080 \
    SQLITE_PATH=/data/url-shortener.db \
    LOG_LEVEL=info

EXPOSE 8080
ENTRYPOINT ["/server"]