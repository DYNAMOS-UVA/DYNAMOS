#!/usr/bin/env bash
# Build the sql-query image and push it to the local registry.
# The Tiltfile runs this script. It does the same steps as the
# "sql-query" target in python/Makefile, but in a temporary directory,
# thus the source tree does not change.
#
# Usage: scripts/tilt-build-sql-query.sh <image-ref>
#   e.g. scripts/tilt-build-sql-query.sh localhost:5001/sql-query:dev
set -euo pipefail

IMAGE="${1:?usage: $0 <image-ref>}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PY="${REPO_ROOT}/python"

STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT

# 1. Service files and the Dockerfile.
cp -r "${PY}/sql-query/." "${STAGE}/"
cp "${PY}/Dockerfile" "${STAGE}/Dockerfile"
cp -r "${PY}/dynamos-python-lib/protofiles" "${STAGE}/protofiles"

# 2. Wheel of dynamos-python-lib (built from a copy of the library).
cp -r "${PY}/dynamos-python-lib" "${STAGE}/.lib"
(cd "${STAGE}/.lib" && python3 setup.py -q bdist_wheel -d "${STAGE}" >/dev/null)
rm -rf "${STAGE}/.lib"
WHEEL_NAME="$(basename "$(ls "${STAGE}"/*.whl)")"

# 3. Build and push.
docker build \
    --build-arg NAME=sql-query \
    --build-arg WHEEL_NAME="${WHEEL_NAME}" \
    -t "${IMAGE}" \
    "${STAGE}"
docker push "${IMAGE}"
