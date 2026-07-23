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

# Serve the fixture DID document (fixture/.well-known/did.json) locally so
# dsp-connector can resolve the DAT's signing DID (issue #56,
# dat_verification.go) during the run. dsp-connector's own resolution call
# runs from the host, not from inside the TCK's container, so plain
# localhost is reachable - no --add-host needed for this one.
python3 -m http.server 9999 --directory "$SCRIPT_DIR/fixture" > /dev/null 2>&1 &
FIXTURE_PID=$!
trap 'kill "$FIXTURE_PID" 2>/dev/null || true' EXIT
sleep 0.5
if ! curl -sf http://localhost:9999/.well-known/did.json > /dev/null; then
  echo "Fixture DID document server failed to start on :9999" >&2
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
