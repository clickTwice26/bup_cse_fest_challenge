# ---- build ----
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/gridwise .

# ---- run ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 app
COPY --from=build /out/gridwise /usr/local/bin/gridwise
USER app
ENV PORT=8000
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:${PORT}/health || exit 1
# No credentials are baked in; keys are supplied at run time with -e.
ENTRYPOINT ["/usr/local/bin/gridwise"]
