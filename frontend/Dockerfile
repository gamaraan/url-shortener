# syntax=docker/dockerfile:1
# Frontend image — multi-stage: Node builds the Svelte SPA, Go embeds the
# built dist/ and compiles the server, a minimal runtime serves it. See
# development.md §3.2/§5.2.

# ---- 1. SPA build stage ----
FROM node:20-alpine AS spa
WORKDIR /src
COPY web/package.json web/package-lock.json* ./web/
RUN cd web && (npm ci --no-audit --no-fund || npm install --no-audit --no-fund)
COPY web/ ./web/
# vite.config.ts builds into ../internal/server/dist (relative to web/),
# i.e. /src/internal/server/dist here.
RUN cd web && npm run build

# ---- 2. Go build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# Overlay the SPA build output into the server package's dist dir for //go:embed.
COPY --from=spa /src/internal/server/dist ./internal/server/dist
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 3. runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app
USER app
COPY --from=build /out/server /server

ENV LISTEN_ADDR=:8080 \
    BACKEND_URL=http://backend:8080 \
    LOG_LEVEL=info

EXPOSE 8080
ENTRYPOINT ["/server"]