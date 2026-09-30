# syntax=docker/dockerfile:1
#
# OnebotNoa — OneBot V11 relay hub. Multi-stage build: the WebUI is compiled by
# Vite, embedded into the Go binary via go:embed, and only the static binary ends
# up in the runtime image.

# ---------------------------------------------------------------- WebUI build
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-fund --no-audit || npm install --no-fund --no-audit
COPY web/ ./
# vite.config.ts writes to ../internal/webui/dist, so the target dir must exist.
RUN mkdir -p /src/internal/webui/dist && npm run build

# ------------------------------------------------------------- Go binary build
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/onebotnoa ./cmd/onebotnoa

# ------------------------------------------------------------------- Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 -h /app onebotnoa && \
    mkdir -p /data && chown -R onebotnoa:onebotnoa /data /app

COPY --from=build /out/onebotnoa /usr/local/bin/onebotnoa

# The default config listens on 127.0.0.1; inside a container that is unreachable
# from the host, so publish on all interfaces unless the operator overrides it.
ENV ONEBOTNOA_LISTEN=0.0.0.0:8080 \
    ONEBOTNOA_SQLITE=/data/onebotnoa.db \
    ONEBOTNOA_LOG_FORMAT=json

USER onebotnoa
WORKDIR /app
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/onebotnoa"]
CMD ["serve", "-config", "/data/config.yaml"]
