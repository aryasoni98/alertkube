#!/bin/bash
set -euo pipefail

TAG="${TAG:-v0.0.1}"
IMAGE="${IMAGE:-aryasoni98/alertkube}"

docker buildx build --platform linux/amd64,linux/arm64 -t "${IMAGE}:${TAG}" --push .
docker tag "${IMAGE}:${TAG}" "${IMAGE}:latest"
docker push "${IMAGE}:latest"
