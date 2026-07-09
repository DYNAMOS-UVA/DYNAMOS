#!/usr/bin/env bash
# Start the DYNAMOS dev container.
# Usage:
#   ./dev.sh               # rebuild image then open a shell
#   ./dev.sh --no-rebuild  # skip rebuild, just open a shell
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Load .env if present
if [[ -f "${SCRIPT_DIR}/.env" ]]; then
    set -a
    # shellcheck source=/dev/null
    source "${SCRIPT_DIR}/.env"
    set +a
fi

IMAGE="${DYNAMOS_DEV_IMAGE:-dynamos-dev}"

if [[ "${1:-}" != "--no-rebuild" ]]; then
    echo "Building image '${IMAGE}' from .devcontainer/Dockerfile ..."
    docker build -f "${SCRIPT_DIR}/.devcontainer/Dockerfile" \
                 -t "${IMAGE}" \
                 "${SCRIPT_DIR}"
fi

echo "Starting container – project mounted at /workspace ..."
docker run -it --rm \
    --name dynamos-dev \
    --network host \
    -v "${SCRIPT_DIR}:/workspace" \
    -v "${HOME}/.kube:/root/.kube:ro" \
    -v "/var/run/docker.sock:/var/run/docker.sock" \
    -e "DYNAMOS_HOST_ROOT=${DYNAMOS_HOST_ROOT:-}" \
    -w /workspace \
    "${IMAGE}" bash
