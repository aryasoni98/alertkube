#!/bin/bash
set -euo pipefail

# Local multi-arch build helper. Production images are built, signed (cosign),
# scanned (Trivy), and given SLSA provenance by .github/workflows/release.yml —
# use the release pipeline for anything published, including the :latest tag.
# Keep IMAGE/TAG in sync with helm/values.yaml.
TAG="${TAG:-v1.0.0}"
IMAGE="${IMAGE:-ghcr.io/aryasoni98/alertkube}"

# By default this only builds (no registry push, no :latest tag — the release
# pipeline owns published/signed tags). Set PUSH=1 to push the :${TAG} tag.
if [[ "${PUSH:-0}" == "1" ]]; then
  docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -t "${IMAGE}:${TAG}" \
    --push .
else
  docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -t "${IMAGE}:${TAG}" .
fi
