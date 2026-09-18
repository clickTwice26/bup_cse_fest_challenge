#!/usr/bin/env bash
# One-command deploy for the GridWise service on a Docker host.
#   ./deploy.sh                      build locally and run on :80
#   ./deploy.sh <registry/name:tag>  also push the image for the Docker fallback
set -euo pipefail

TAG="${1:-gridwise:local}"
PORT_OUT="${PORT_OUT:-80}"

if [ ! -f .env ]; then
  echo "ERROR: .env not found. Copy .env.example to .env and add your key(s)." >&2
  exit 1
fi

echo "==> building $TAG"
docker build -t "$TAG" .

if [ "$TAG" != "gridwise:local" ]; then
  echo "==> pushing $TAG"
  docker push "$TAG"
  echo "==> digest:"
  docker inspect --format='{{index .RepoDigests 0}}' "$TAG" || true
fi

echo "==> restarting container"
docker rm -f gridwise >/dev/null 2>&1 || true
docker run -d --name gridwise --restart=always \
  -p "${PORT_OUT}:8000" --env-file .env "$TAG"

echo "==> waiting for health"
for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${PORT_OUT}/health" >/dev/null 2>&1; then
    echo "healthy after ${i}s"
    curl -s "http://127.0.0.1:${PORT_OUT}/health"; echo
    exit 0
  fi
  sleep 1
done
echo "ERROR: service did not become healthy in 30s" >&2
docker logs --tail 40 gridwise >&2
exit 1
