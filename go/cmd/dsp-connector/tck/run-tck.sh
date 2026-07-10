#!/usr/bin/env bash
# Runs the Eclipse Dataspace Protocol TCK's Docker image against a locally
# running dsp-connector (go run -tags local . on :8090). See T1.3 (issue #21).
#
# Usage: ./run-tck.sh [path-to-log-file]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_FILE="${1:-$SCRIPT_DIR/last-run.log}"

if ! curl -sf http://localhost:8090/health > /dev/null; then
  echo "dsp-connector is not reachable on :8090 - start it first:" >&2
  echo "  (cd go/cmd/dsp-connector && go run -tags local .)" >&2
  exit 1
fi

docker run --rm --name dsp-tck \
  --add-host "host.docker.internal:host-gateway" \
  --mount "type=bind,source=$SCRIPT_DIR/tck.properties,target=/etc/tck/config.properties" \
  eclipsedataspacetck/dsp-tck-runtime:latest \
  | tee "$LOG_FILE"

echo
echo "Full log written to $LOG_FILE"
echo "CAT group results:"
grep -E "^(CAT|MET|CN|TP)[:_]" "$LOG_FILE" || echo "  (no group-prefixed result lines found - inspect $LOG_FILE)"
