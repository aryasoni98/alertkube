#!/bin/bash
set -euo pipefail

# Local multi-arch build & push. CI release builds happen in
# .github/workflows/release.yml; keep IMAGE in sync with helm/values.yaml.
TAG="${TAG:-v0.0.1}"
IMAGE="${IMAGE:-ghcr.io/aryasoni98/alertkube}"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t "${IMAGE}:${TAG}" \
  -t "${IMAGE}:latest" \
  --push .
