#!/usr/bin/env bash
# Build and push Docker images for only the microservices whose source
# changed between two commits. Used by .github/workflows/publish-images.yml
# on push to main. Delegates the actual build+push to go/Makefile and
# python/Makefile, which already tag and push to the dynamos1 Docker Hub
# account.
set -euo pipefail

BASE_SHA="${1:-}"
HEAD_SHA="${2:-HEAD}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# actions/checkout leaves a detached HEAD, so `git rev-parse --abbrev-ref
# HEAD` inside the Makefiles would resolve to "HEAD" instead of "main" and
# push wrongly tagged images. This workflow only ever runs on push to main,
# so pin the tag explicitly and pass it through as a make override.
BRANCH_NAME=main

# BASE_SHA is unusable on a repo's first push to a ref (or after a
# force-push): GitHub sends the all-zero SHA, or a SHA no longer reachable.
# Treat every microservice as changed rather than fail the build.
if [[ -z "$BASE_SHA" || "$BASE_SHA" =~ ^0+$ ]] || ! git cat-file -e "$BASE_SHA" 2>/dev/null; then
    echo "No usable previous commit - treating all microservices as changed."
    changed="$(git ls-files)"
else
    changed="$(git diff --name-only "$BASE_SHA" "$HEAD_SHA")"
fi

echo "Changed files:"
echo "$changed"
echo

matches() {
    echo "$changed" | grep -qE "$1"
}

# Changes here can affect every go microservice image, since go/Makefile
# copies go.mod, go.sum, pkg/ and Dockerfile into each service's build
# context before building it.
go_shared='^go/(pkg/|go\.mod$|go\.sum$|Dockerfile$|Makefile$)|^proto-files/'
go_targets=(sidecar policy-enforcer orchestrator agent api-gateway sql-algorithm sql-anonymize sql-aggregate sql-test dsp-connector catalog-service negotiation-service transfer-process-service)

# Changes here can affect the python microservice image, since
# python/Makefile bundles dynamos-python-lib into the service's build
# context before building it.
py_shared='^python/(dynamos-python-lib/|Dockerfile$|Makefile$)|^proto-files/'
py_targets=(sql-query)

build_go=()
if matches "$go_shared"; then
    build_go=("${go_targets[@]}")
else
    for t in "${go_targets[@]}"; do
        matches "^go/cmd/${t}/" && build_go+=("$t")
    done
fi

build_py=()
if matches "$py_shared"; then
    build_py=("${py_targets[@]}")
else
    for t in "${py_targets[@]}"; do
        matches "^python/${t}/" && build_py+=("$t")
    done
fi

if [ ${#build_go[@]} -eq 0 ] && [ ${#build_py[@]} -eq 0 ]; then
    echo "No microservice source changed. Nothing to build."
    exit 0
fi

if [ ${#build_go[@]} -gt 0 ]; then
    echo "Building go image(s): ${build_go[*]}"
    make -C go branch_name="$BRANCH_NAME" "${build_go[@]}"
fi

if [ ${#build_py[@]} -gt 0 ]; then
    echo "Building python image(s): ${build_py[*]}"
    make -C python branch_name="$BRANCH_NAME" "${build_py[@]}"
fi
