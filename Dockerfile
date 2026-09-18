# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.27-alpine AS build
WORKDIR /src
# Manifests first so the dependency layer caches independently of source edits.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Static binary: no libc dependency, so the final stage stays minimal.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gridwise ./cmd/server

# ---- run ----
FROM alpine:3.20
# ca-certificates is required: the service calls the Gemini/Groq HTTPS APIs.
# wget backs the container HEALTHCHECK only.
RUN apk add --no-cache ca-certificates wget \
 && adduser -D -u 10001 app
COPY --from=build /out/gridwise /usr/local/bin/gridwise
USER app
# Default port; the platform overrides PORT at runtime and the server reads it.
ENV PORT=8000
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" || exit 1
# No credentials are baked in - API keys are supplied at run time.
# Exec form so the server is PID 1 and receives signals directly.
ENTRYPOINT ["/usr/local/bin/gridwise"]
