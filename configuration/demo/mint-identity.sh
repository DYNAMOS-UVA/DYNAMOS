#!/bin/bash
# Mints a fresh DSP demo identity: new ES256 keypair, a did:web document
# published to the already-deployed fixture-did pod (dsp-connector
# namespace), a signed DAT for it, and a matching Relation seeded into VU's
# real policyEnforcer agreement key. Run this once before firing the Postman
# demo chain (configuration/demo/DYNAMOS-DSP-Demo.postman_collection.json) -
# paste the printed DAT into the Postman environment's "dat" variable.
#
# Assumes: dynamos-configuration.sh has already been run against a live
# kind-dynamos cluster (fixture-did pod + negotiation-service + dsp-connector
# all deployed), and kubectl's current context can reach it.
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_DIR="${SCRIPT_DIR}/../../go"

echo "Port-forwarding etcd..."
kubectl -n core port-forward svc/etcd 2379:2379 > /tmp/dsp-demo-etcd-pf.log 2>&1 &
PF_PID=$!
trap 'kill ${PF_PID} 2>/dev/null || true' EXIT

for i in $(seq 1 15); do
    curl -s -o /dev/null http://localhost:2379/health && break
    sleep 1
done

echo "Generating keypair + DID document + DAT, seeding etcd Relation..."
(cd "${GO_DIR}" && go run "${SCRIPT_DIR}/../../go/cmd/dsp-connector/demo/mint_identity.go")

echo ""
echo "Publishing DID document to the fixture-did pod..."
kubectl -n dsp-connector create configmap fixture-did \
    --from-file=did.json=/tmp/dsp-demo-did.json \
    --dry-run=client -o yaml | kubectl -n dsp-connector apply -f -
kubectl -n dsp-connector rollout restart deployment/fixture-did
kubectl -n dsp-connector rollout status deployment/fixture-did --timeout=60s

echo ""
echo "Done. Paste the DAT above into the Postman environment's 'dat' variable, then run the collection."
