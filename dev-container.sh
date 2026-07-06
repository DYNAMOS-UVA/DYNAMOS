#!/usr/bin/env bash
# Build and run the DYNAMOS dev container.
# Usage:
#   ./dev-container.sh          # rebuild image and open a shell
#   ./dev-container.sh --no-rebuild   # skip rebuild, just open a shell
set -euo pipefail

IMAGE="dynamos-dev"
PROJECT_ROOT="$(cd "$(dirname "$0")" && pwd)"

if [[ "${1:-}" != "--no-rebuild" ]]; then
    echo "Building image '${IMAGE}' from .devcontainer/Dockerfile ..."
    docker build -f "${PROJECT_ROOT}/.devcontainer/Dockerfile" \
                 -t "${IMAGE}" \
                 "${PROJECT_ROOT}"
fi

echo "Starting container – project mounted at /workspace ..."
docker run -it --rm \
    --name dynamos-dev \
    --network host \
    -v "${PROJECT_ROOT}:/workspace" \
    -v "${HOME}/.kube:/root/.kube:ro" \
    -v "/var/run/docker.sock:/var/run/docker.sock" \
    -w /workspace \
    "${IMAGE}" bash
