#!/bin/bash
# Interactive DSP Consumer-role demo for issue #93: this identity (VU)
# consumes real data from UVA as Provider - real DYNAMOS-to-DYNAMOS DSP
# negotiation and transfer, no throwaway echo-listener pod. Menu-driven so
# every state transition is visible, instead of firing a fixed script -
# same request chain that was hand-verified live for #93/#97 (see the
# wiki session notes), just interactive instead of a Postman collection.
#
# Prerequisites:
#   - run inside the project's own devcontainer (./dev.sh) - that's where
#     kubectl/helm/docker already live.
#   - run configuration/demo/setup-demos.sh once first (or again after the
#     cluster itself was recreated) - this script has no setup step of its
#     own any more. That script covers dynamos-configuration.sh, agent's
#     own dsp-latest image tag (issue #97's job-name fix isn't merged to
#     main yet), and the env vars no chart wires up yet (CONNECTOR_BASE_URL,
#     DID_WEB_SCHEME on dsp-connector).
#   - everything else (minting an identity, syncing PARTY_DAT, seeding
#     UVA's starter policyEnforcer Relation, the fixture-did identity
#     server if it's ever missing) happens automatically on every plain
#     run, no separate step needed.
#
# No set -e: this is an interactive loop, one failed request must not kill
# the whole session.
set -uo pipefail

SCRATCH_DIR="${TMPDIR:-/tmp}/dsp-transfer-demo"
mkdir -p "${SCRATCH_DIR}"

# ---------------------------------------------------------------------------
# Config - matches the request chain hand-verified live for #93/#97
# ---------------------------------------------------------------------------
INGRESS_PORT=8080
NEG_VU_PORT=8092
NEG_UVA_PORT=9092
ETCD_PORT=2379

INGRESS_URL="http://localhost:${INGRESS_PORT}"
NEG_VU_URL="http://localhost:${NEG_VU_PORT}"
NEG_UVA_URL="http://localhost:${NEG_UVA_PORT}"

VU_DSP_HOST="dsp-vu.dsp-connector.svc.cluster.local"
UVA_DSP_HOST="dsp-uva.dsp-connector.svc.cluster.local"
UVA_CONNECTOR_ADDRESS="http://dsp-connector-uva.dsp-connector.svc.cluster.local:8080/api/v1"
DID="did:web:fixture-did.dsp-connector.svc.cluster.local"

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
log_step() { echo; echo "=== $* ==="; }
log_ok()   { echo "  [ok] $*"; }
log_info() { echo "  [..] $*"; }
log_fail() { echo "  [FAIL] $*"; }
log_json() { echo "$1" | jq . 2>/dev/null || echo "$1"; }

# curl_retry_post retries a POST up to 3 times, 2s apart, if the transport
# itself fails (empty response) or the response is a transient
# "upstream-error" (a service-to-service call inside the cluster failed).
# Live-found running this script for real: negotiation-service-vu and
# UVA's dsp-connector are reachable most of the time but not always
# instantly after the whole stack has just come up - same class of
# flakiness the rebuild-redeploy sessions hit with port-forwards. Only
# used for the two chain-critical calls (negotiation/transfer initiate) -
# a transient failure there has no easy recovery via a later poll, unlike
# every other request in this script.
## Retries only on a genuinely empty response (curl itself failed to
## connect - a port-forward mid-rollout, a pod not quite ready yet). A
## response that decoded but carries an error "code" is a real, deterministic
## rejection (bad request, wrong state) - retrying that wastes time showing
## the same answer, so it is returned immediately instead.
curl_retry_post() {
    local url="$1" data="$2"
    shift 2
    local resp attempt
    for attempt in 1 2 3 4; do
        resp=$(curl -s "$@" -X POST "$url" -d "$data")
        if [[ -n "$resp" ]]; then
            echo "$resp"
            return 0
        fi
        if [[ "$attempt" -lt 4 ]]; then
            log_info "empty response (attempt ${attempt}/4, likely a port-forward/pod not ready yet), retrying in 2s..." >&2
            sleep 2
        fi
    done
    echo "$resp"
}

# ---------------------------------------------------------------------------
# State - persists across menu choices within this run
# ---------------------------------------------------------------------------
DAT=""
UVA_OFFER_ID=""
UVA_DATASET_ID=""
NEG_PROVIDER_PID=""
NEG_PROVIDER_PID_ENC=""
NEG_CONSUMER_PID=""
NEG_STATE=""
TRANSFER_CONSUMER_PID=""
TRANSFER_CONSUMER_PID_ENC=""
TRANSFER_STATE=""

# ---------------------------------------------------------------------------
# Setup: dependencies, identity, port-forwards
# ---------------------------------------------------------------------------
require_tools() {
    for tool in jq curl kubectl docker; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            log_fail "required tool not found: ${tool}"
            exit 1
        fi
    done
}

# ensure_fixture_did creates the fixture-did Deployment+Service if either
# is missing. Nothing in this repo does this today: no Helm chart, no
# tracked manifest anywhere (confirmed - grepped the whole repo for it).
# mint-identity.sh (and the Go program it calls) both only *restart* this
# Deployment to pick up a freshly-minted DID document into its ConfigMap -
# they assume it already exists, because on every session before this one
# it always had, created ad hoc at some earlier point and never saved.
# Live-found running --setup for real against a genuinely fresh cluster:
# "deployments.apps fixture-did not found". A minimal nginx pod serving
# the ConfigMap-mounted did.json at the exact path did:web resolution
# requests (dat_verification.go's own resolveDIDURL) - no linkerd
# injection (matches the original's own observed 1/1, not 2/2, shape),
# nothing DSP-specific about this beyond serving one static file. Safe to
# call every time: kubectl apply is a no-op if it already matches.
ensure_fixture_did() {
    if kubectl -n dsp-connector get deployment fixture-did >/dev/null 2>&1 \
        && kubectl -n dsp-connector get svc fixture-did >/dev/null 2>&1; then
        return 0
    fi
    log_step "Creating the fixture-did Deployment+Service (missing, not tracked in any chart)"

    kubectl -n dsp-connector get configmap fixture-did >/dev/null 2>&1 \
        || kubectl -n dsp-connector create configmap fixture-did --from-literal=did.json='{}' >/dev/null

    kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fixture-did
  namespace: dsp-connector
  labels:
    app: fixture-did
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fixture-did
  template:
    metadata:
      labels:
        app: fixture-did
      annotations:
        linkerd.io/inject: disabled
    spec:
      containers:
        - name: fixture-did
          image: nginx:1.27-alpine
          ports:
            - containerPort: 80
          volumeMounts:
            - name: did-doc
              mountPath: /usr/share/nginx/html/.well-known
              readOnly: true
      volumes:
        - name: did-doc
          configMap:
            name: fixture-did
            items:
              - key: did.json
                path: did.json
---
apiVersion: v1
kind: Service
metadata:
  name: fixture-did
  namespace: dsp-connector
spec:
  selector:
    app: fixture-did
  ports:
    - port: 80
      targetPort: 80
EOF

    if kubectl -n dsp-connector rollout status deployment/fixture-did --timeout=60s >/dev/null 2>&1; then
        log_ok "fixture-did created"
    else
        log_fail "fixture-did did not come up - is the cluster actually reachable? (see errors above)"
        return 1
    fi
}

get_dat() {
    if [[ -n "${DAT_ENV:-}" ]]; then
        DAT="${DAT_ENV}"
        log_ok "using DAT from \$DAT_ENV"
        return
    fi

    ensure_fixture_did

    log_step "Minting a fresh identity"
    local mint_output="${SCRATCH_DIR}/mint-identity-output.txt"
    "$(dirname "$0")/mint-identity.sh" 2>&1 | tee "$mint_output"
    # mint-identity.sh prints "DAT:" on its own line, then the token on the
    # next - pull it straight from its own output instead of asking the
    # operator to copy/paste it, same identity either way.
    DAT=$(awk '/^DAT:$/{getline; print; exit}' "$mint_output")

    if [[ -z "$DAT" ]]; then
        log_fail "could not extract DAT from mint-identity.sh output - see ${mint_output}"
        exit 1
    fi
    log_ok "DAT captured"
}

# sync_party_dat pushes the just-minted DAT into negotiation-service's and
# transfer-process-service's own PARTY_DAT env var (both parties) - the
# credential each attaches to its own outbound pushes (issue #97's own
# fix). mint-identity.sh generates a fresh signing key on every run, so a
# PARTY_DAT left over from an earlier identity silently fails DAT
# verification on the receiving end - outbound Offer/Agreement/Start
# deliveries get logged and dropped, the Consumer-role record just never
# advances past its current state. Live-found running this script for
# real: negotiation stuck oscillating between REQUESTED and OFFERED,
# because UVA's own outbound push to this identity's callback kept
# 401'ing against a stale key. kubectl set env is a no-op (no rollout) if
# the value did not actually change, so this costs nothing extra on a
# repeat run with the same identity.
sync_party_dat() {
    log_step "Syncing PARTY_DAT to the just-minted identity"
    local ns_dep ns dep
    for ns_dep in \
        "negotiation-service:negotiation-service-uva" \
        "negotiation-service:negotiation-service-vu" \
        "transfer-process-service:transfer-process-service-uva" \
        "transfer-process-service:transfer-process-service-vu"
    do
        ns="${ns_dep%%:*}"; dep="${ns_dep##*:}"
        kubectl -n "$ns" set env "deployment/${dep}" "PARTY_DAT=${DAT}" >/dev/null
    done
    for ns_dep in \
        "negotiation-service:negotiation-service-uva" \
        "negotiation-service:negotiation-service-vu" \
        "transfer-process-service:transfer-process-service-uva" \
        "transfer-process-service:transfer-process-service-vu"
    do
        ns="${ns_dep%%:*}"; dep="${ns_dep##*:}"
        kubectl -n "$ns" rollout status "deployment/${dep}" --timeout=60s >/dev/null 2>&1
    done

    # A port-forward to svc/negotiation-service-{uva,vu} was resolved to a
    # specific pod when it was first started (during the earlier
    # health_check) - it does not follow the rollout above onto the new
    # pod, even addressed by service name. Force-kill both here so the
    # next health_check call always starts fresh against the pod that is
    # actually running now, rather than intermittently reusing a
    # connection that is about to (or already did) go stale. Live-found
    # running this script for real: negotiation stuck oscillating between
    # REQUESTED and OFFERED because this exact port-forward kept answering
    # right up until it silently didn't.
    pkill -9 -f "port-forward svc/negotiation-service-uva ${NEG_UVA_PORT}:8080" 2>/dev/null
    pkill -9 -f "port-forward svc/negotiation-service-vu ${NEG_VU_PORT}:8080" 2>/dev/null

    log_ok "PARTY_DAT synced on both parties"
}

# ensure_port_forward checks a local port with a real HTTP request (not just
# TCP-listen) and starts kubectl port-forward in the background if it isn't
# already answering. setsid detaches it fully - a plain "&" here left
# processes that silently died once the parent shell moved on, live-found
# building this script. Kills any existing port-forward for this exact
# service first: a port-forward whose backing pod just died (e.g. from
# sync_party_dat's own rollout) can still hold the local port without
# answering, and a second kubectl port-forward then fails to bind at all -
# also live-found building this script.
ensure_port_forward() {
    local ns="$1" svc="$2" local_port="$3" remote_port="$4" check_path="$5" host_header="${6:-}"
    local -a host_arg=()
    [[ -n "$host_header" ]] && host_arg=(-H "Host: ${host_header}")

    if curl -sf --max-time 2 "${host_arg[@]}" "http://localhost:${local_port}${check_path}" >/dev/null 2>&1; then
        log_ok "localhost:${local_port} -> ${ns}/${svc}"
        return 0
    fi

    pkill -9 -f "port-forward svc/${svc} ${local_port}:${remote_port}" 2>/dev/null
    sleep 1

    log_info "starting port-forward ${ns}/${svc} ${local_port}:${remote_port} ..."
    setsid kubectl -n "$ns" port-forward "svc/${svc}" "${local_port}:${remote_port}" \
        > "${SCRATCH_DIR}/pf-${svc}.log" 2>&1 < /dev/null &
    disown

    for _ in $(seq 1 10); do
        sleep 1
        if curl -sf --max-time 2 "${host_arg[@]}" "http://localhost:${local_port}${check_path}" >/dev/null 2>&1; then
            log_ok "localhost:${local_port} -> ${ns}/${svc} (started)"
            return 0
        fi
    done
    log_fail "could not reach localhost:${local_port} -> ${ns}/${svc} (see ${SCRATCH_DIR}/pf-${svc}.log)"
    return 1
}

health_check() {
    log_step "Cluster reachability"
    if kubectl get nodes >/dev/null 2>&1; then
        log_ok "kubectl reaches the cluster"
    else
        log_fail "kubectl cannot reach the cluster - is it running? (docker start dynamos-control-plane)"
        return 1
    fi

    log_step "Port-forwards"
    local all_ok=true
    ensure_port_forward ingress nginx-nginx-ingress-controller "$INGRESS_PORT" 80 \
        "/api/v1/.well-known/dspace-version" "$VU_DSP_HOST" || all_ok=false
    ensure_port_forward negotiation-service negotiation-service-vu "$NEG_VU_PORT" 8080 "/health" || all_ok=false
    ensure_port_forward negotiation-service negotiation-service-uva "$NEG_UVA_PORT" 8080 "/health" || all_ok=false
    ensure_port_forward core etcd "$ETCD_PORT" 2379 "/health" || all_ok=false

    log_step "Service health (through ingress, both parties)"
    if curl -sf --max-time 3 -H "Host: ${VU_DSP_HOST}" "${INGRESS_URL}/api/v1/.well-known/dspace-version" >/dev/null; then
        log_ok "VU dsp-connector reachable"
    else
        log_fail "VU dsp-connector not reachable through ingress"
        all_ok=false
    fi
    if curl -sf --max-time 3 -H "Host: ${UVA_DSP_HOST}" "${INGRESS_URL}/api/v1/.well-known/dspace-version" >/dev/null; then
        log_ok "UVA dsp-connector reachable"
    else
        log_fail "UVA dsp-connector not reachable through ingress"
        all_ok=false
    fi

    if [[ "$all_ok" == "true" ]]; then
        log_ok "all checks passed"
    else
        echo
        log_fail "one or more checks failed - fix before continuing, or expect requests below to fail"
    fi
}

# ---------------------------------------------------------------------------
# etcd helper (docker-based etcdctl - matches the exact pattern used to
# hand-verify #93/#97, since etcdctl isn't installed on the host here)
# ---------------------------------------------------------------------------
etcd_get() {
    docker run --rm --network host quay.io/coreos/etcd:v3.5.1 \
        etcdctl --endpoints="http://localhost:${ETCD_PORT}" get "$1" --print-value-only
}
etcd_put() {
    docker run --rm -i --network host quay.io/coreos/etcd:v3.5.1 \
        etcdctl --endpoints="http://localhost:${ETCD_PORT}" put "$1"
}

seed_uva_relation() {
    log_step "Seeding a starter Relation for ${DID} on UVA's policyEnforcer"
    log_info "one-time prerequisite - the negotiation's own FINALIZED step later"
    log_info "self-heals this with the real negotiated constraints (T2.4 deriveRelation)"

    local current merged
    current=$(etcd_get "/policyEnforcer/agreements/UVA")
    if [[ -z "$current" ]]; then
        log_fail "could not read /policyEnforcer/agreements/UVA - is the etcd port-forward up?"
        return 1
    fi

    merged=$(echo "$current" | jq --arg did "$DID" '
        .relations[$did] = {
            "ID": "GUID",
            "requestTypes": ["sqlDataRequest", "genericRequest"],
            "dataSets": ["wageGap", "employeeSurvey"],
            "allowedArchetypes": ["computeToData", "dataThroughTtp"],
            "allowedComputeProviders": ["SURF"]
        }')

    if echo "$merged" | etcd_put "/policyEnforcer/agreements/UVA" >/dev/null; then
        log_ok "seeded - UVA now recognizes ${DID}"
    else
        log_fail "etcd write failed"
    fi
}

# seed_employee_survey_dataset registers the "employeeSurvey" dataset
# (single table "Employees": EmployeeId/Department/YearsExperience/Salary/
# RemoteWorker/Country - a fictional, English, human-readable dataset, added
# alongside wageGap so a query is easy to write and understand without Dutch
# HR-system column names) directly into etcd's own /datasets/employeeSurvey
# key. Not added to configuration/etcd_launch_files/datasets.json: that file
# only matters if pushed to the real GitHub main branch - orchestrator's own
# prod build reads it from an init Job that curls
# raw.githubusercontent.com/DYNAMOS-UVA/DYNAMOS/main/..., not the local
# working tree (confirmed live - editing the local file alone has zero
# effect on a running cluster). A direct etcd write is the same approach
# seed_uva_relation already uses for the Relation, for the same reason.
# Safe to call every run: overwrites with the same value if unchanged.
seed_employee_survey_dataset() {
    log_step "Seeding the employeeSurvey dataset (Employees table)"
    if echo '{"name":"employeeSurvey","type":"csv","delimiter":";","tables":["Employees"]}' \
        | etcd_put "/datasets/employeeSurvey" >/dev/null; then
        log_ok "employeeSurvey dataset registered"
    else
        log_fail "etcd write failed"
    fi
}

# ---------------------------------------------------------------------------
# 1) Catalog
# ---------------------------------------------------------------------------
do_catalog() {
    log_step "Requesting UVA's catalog (as Consumer)"
    local resp
    resp=$(curl -s -H "Host: ${UVA_DSP_HOST}" -H "Authorization: Bearer ${DAT}" -H "Content-Type: application/json" \
        -X POST "${INGRESS_URL}/api/v1/catalog/request" -d '{}')
    log_json "$resp"

    if echo "$resp" | jq -e '.dataset[0]' >/dev/null 2>&1; then
        UVA_DATASET_ID=$(echo "$resp" | jq -r '.dataset[0]."@id"')
        UVA_OFFER_ID=$(echo "$resp" | jq -r '.dataset[0].hasPolicy[0]."@id"')
        log_ok "dataset=${UVA_DATASET_ID}"
        log_ok "offer=${UVA_OFFER_ID}"
    else
        log_fail "no dataset offered - this identity should already be seeded (setup does it"
        log_fail "automatically); re-run the script if this persists."
    fi
}

# ---------------------------------------------------------------------------
# 2) Negotiation - state-machine-driven submenu
# ---------------------------------------------------------------------------
neg_initiate() {
    if [[ -z "$UVA_OFFER_ID" || -z "$UVA_DATASET_ID" ]]; then
        log_fail "no offer/dataset yet - run 'Request Catalog' (main menu 1) first"
        return
    fi
    log_step "Initiating negotiation as Consumer"
    local body resp
    body=$(jq -n --arg pid "UVA" --arg offer "$UVA_OFFER_ID" --arg ds "$UVA_DATASET_ID" --arg addr "$UVA_CONNECTOR_ADDRESS" \
        '{providerId: $pid, offerId: $offer, datasetId: $ds, connectorAddress: $addr}')
    resp=$(curl_retry_post "${INGRESS_URL}/api/v1/negotiations/initiate" "$body" \
        -H "Host: ${VU_DSP_HOST}" -H "Authorization: Bearer ${DAT}" -H "Content-Type: application/json")
    log_json "$resp"

    if echo "$resp" | jq -e 'type == "object" and (has("code")|not)' >/dev/null 2>&1; then
        NEG_PROVIDER_PID=$(echo "$resp" | jq -r '.providerPid')
        NEG_PROVIDER_PID_ENC=$(jq -rn --arg p "$NEG_PROVIDER_PID" '$p|@uri')
        NEG_CONSUMER_PID=$(echo "$resp" | jq -r '.consumerPid')
        NEG_STATE=$(echo "$resp" | jq -r '.state')
        log_ok "UVA providerPid=${NEG_PROVIDER_PID}"
        log_ok "own consumerPid=${NEG_CONSUMER_PID}"
    else
        log_fail "initiate failed"
    fi
}

neg_send_offer() {
    log_step "Sending Offer (playing UVA/Provider side)"
    local body resp
    body=$(jq -n --arg id "$UVA_OFFER_ID" --arg target "$UVA_DATASET_ID" \
        '{offer: {"@id": $id, "@type": "Offer", target: $target, permission: [{action: "dynamos:sqlDataRequest"}]}}')
    resp=$(curl_retry_post "${NEG_UVA_URL}/internal/v1/negotiations/${NEG_PROVIDER_PID_ENC}/offer" "$body" \
        -H "Content-Type: application/json")
    log_json "$resp"
    [[ "$(echo "$resp" | jq -r '.state // empty')" == "OFFERED" ]] && NEG_STATE="OFFERED"
    log_info "consumerAutoNegotiate should auto-accept within ~1-2s - poll to see it"
}

neg_send_agreement() {
    log_step "Sending Agreement (playing UVA/Provider side, real constraints)"
    log_info "using real archetype/computeProvider constraints - a minimal Agreement here"
    log_info "would self-heal UVA's Relation into an unusable state (issue #97's own finding)"
    local body resp
    body=$(jq -n --arg target "$UVA_DATASET_ID" --arg assignee "$DID" '{
        agreement: {
            "@id": "urn:dynamos:agreement:UVA:demo-script",
            "@type": "Agreement",
            target: $target,
            assigner: "urn:dynamos:party:UVA",
            assignee: $assignee,
            permission: [{
                action: "dynamos:sqlDataRequest",
                constraint: [
                    {leftOperand: "dynamos:archetype", operator: "isAnyOf", rightOperand: ["computeToData", "dataThroughTtp"]},
                    {leftOperand: "dynamos:computeProvider", operator: "isAnyOf", rightOperand: ["SURF"]}
                ]
            }]
        }
    }')
    resp=$(curl_retry_post "${NEG_UVA_URL}/internal/v1/negotiations/${NEG_PROVIDER_PID_ENC}/agreement" "$body" \
        -H "Content-Type: application/json")
    log_json "$resp"
    [[ "$(echo "$resp" | jq -r '.state // empty')" == "AGREED" ]] && NEG_STATE="AGREED"
    log_info "consumerAutoNegotiate should auto-verify within ~1-2s - poll to see it"
}

neg_send_finalize() {
    log_step "Sending FINALIZED event (playing UVA/Provider side)"
    local resp
    resp=$(curl_retry_post "${NEG_UVA_URL}/internal/v1/negotiations/${NEG_PROVIDER_PID_ENC}/events" '{"eventType":"FINALIZED"}' \
        -H "Content-Type: application/json")
    log_json "$resp"
    [[ "$(echo "$resp" | jq -r '.state // empty')" == "FINALIZED" ]] && NEG_STATE="FINALIZED"
    log_ok "UVA's policyEnforcer Relation for this identity now reflects the real negotiated constraints"
}

neg_poll() {
    if [[ -z "$NEG_CONSUMER_PID" ]]; then
        log_fail "no negotiation started yet"
        return
    fi
    log_step "Polling own negotiation state"
    local enc resp
    enc=$(jq -rn --arg p "$NEG_CONSUMER_PID" '$p|@uri')
    resp=$(curl -s -H "Host: ${VU_DSP_HOST}" -H "Authorization: Bearer ${DAT}" \
        "${INGRESS_URL}/api/v1/callback/negotiations/${enc}")
    log_json "$resp"
    NEG_STATE=$(echo "$resp" | jq -r '.state // empty')
}

negotiation_menu() {
    while true; do
        echo
        echo "--- Negotiation (state: ${NEG_STATE:-none}) ---"
        case "$NEG_STATE" in
            "")
                echo "1) Initiate negotiation (as Consumer)"
                ;;
            REQUESTED)
                echo "1) Send Offer (playing UVA/Provider side)"
                ;;
            OFFERED)
                echo "(waiting on this side's own auto-accept - poll to check)"
                ;;
            ACCEPTED)
                echo "1) Send Agreement (playing UVA/Provider side)"
                ;;
            AGREED)
                echo "(waiting on this side's own auto-verify - poll to check)"
                ;;
            VERIFIED)
                echo "1) Send FINALIZED event (playing UVA/Provider side)"
                ;;
            FINALIZED)
                echo "FINALIZED - ready for the Transfer menu (main menu 3)"
                ;;
        esac
        echo "p) Poll current status"
        echo "0) Back to main menu"
        read -rp "> " choice
        case "${NEG_STATE}:${choice}" in
            ":1") neg_initiate ;;
            "REQUESTED:1") neg_send_offer ;;
            "ACCEPTED:1") neg_send_agreement ;;
            "VERIFIED:1") neg_send_finalize ;;
            *:p|*:P) neg_poll ;;
            *:0) return ;;
            *) echo "  (not a valid choice for the current state)" ;;
        esac
    done
}

# ---------------------------------------------------------------------------
# 3) Transfer - state-machine-driven submenu
# ---------------------------------------------------------------------------
## choose_data_address prints ONLY the final JSON (or the literal string
## "null") to stdout - it is called as `data_address=$(choose_data_address)`,
## so command substitution captures everything the function writes to
## stdout. Every prompt/menu line here must go to stderr (>&2), or it gets
## glued onto the JSON and corrupts it - live-found running this script for
## real, the very bug this comment is warning about.
choose_data_address() {
    echo >&2
    echo "What to transfer:" >&2
    echo "  1) Default demo query - wageGap (Personen JOIN Aanstellingen, average by Geslacht)" >&2
    echo "  2) employeeSurvey query (Employees table, real per-row data)" >&2
    echo "  3) Custom query" >&2
    echo "  4) No query (job-less transfer - stays REQUESTED, proves the protocol layer only)" >&2
    read -rp "> " choice
    case "$choice" in
        2)
            # sql-algorithm's own dispatch (go/cmd/sql-algorithm/application_logic.go)
            # only special-cases algorithm=="average", hardcoded to wageGap's own
            # column names (Genders/Salaries) - anything else falls through to its
            # generic convertAllData pass-through, which works for any columns.
            # employeeSurvey's own "average" would hit that hardcoding and come
            # back empty (live-found running this script for real) - "raw" here is
            # just a label, any non-"average" value takes the generic path.
            jq -n '{query: "SELECT * FROM Employees LIMIT 20", algorithm: "raw", algorithmColumns: {}}'
            ;;
        3)
            local query algorithm columns
            read -rp "SQL query: " query
            read -rp "Algorithm (e.g. average): " algorithm
            read -rp "Algorithm columns as JSON (e.g. {\"Geslacht\":\"Aanst_22, Gebdat\"}): " columns
            jq -n --arg q "$query" --arg a "$algorithm" --argjson c "${columns:-{}}" \
                '{query: $q, algorithm: $a, algorithmColumns: $c}'
            ;;
        4)
            echo "null"
            ;;
        *)
            jq -n '{query: "SELECT * FROM Personen p JOIN Aanstellingen s LIMIT 50", algorithm: "average", algorithmColumns: {"Geslacht": "Aanst_22, Gebdat"}}'
            ;;
    esac
}

transfer_initiate() {
    if [[ "$NEG_STATE" != "FINALIZED" ]]; then
        log_fail "negotiation is not FINALIZED yet (state: ${NEG_STATE:-none}) - finish the Negotiation menu first"
        return
    fi
    local data_address body resp
    data_address=$(choose_data_address)

    log_step "Initiating transfer as Consumer"
    if [[ "$data_address" == "null" ]]; then
        body=$(jq -n --arg neg "$NEG_CONSUMER_PID" '{negotiationId: $neg, format: "dynamos:sqlDataRequest"}')
    else
        body=$(jq -n --arg neg "$NEG_CONSUMER_PID" --argjson da "$data_address" \
            '{negotiationId: $neg, format: "dynamos:sqlDataRequest", dataAddress: $da}')
    fi
    resp=$(curl_retry_post "${INGRESS_URL}/api/v1/transfers/initiate" "$body" \
        -H "Host: ${VU_DSP_HOST}" -H "Authorization: Bearer ${DAT}" -H "Content-Type: application/json")
    log_json "$resp"

    if echo "$resp" | jq -e 'type == "object" and (has("code")|not)' >/dev/null 2>&1; then
        TRANSFER_CONSUMER_PID=$(echo "$resp" | jq -r '.consumerPid')
        TRANSFER_CONSUMER_PID_ENC=$(jq -rn --arg p "$TRANSFER_CONSUMER_PID" '$p|@uri')
        TRANSFER_STATE=$(echo "$resp" | jq -r '.state')
        log_ok "own consumerPid=${TRANSFER_CONSUMER_PID}"
        if [[ "$data_address" == "null" ]]; then
            log_info "job-less transfer - stays REQUESTED, no job ever triggers (by design)"
        else
            log_info "real job triggered on UVA's side - takes ~10-15s, poll for COMPLETED"
        fi
    else
        log_fail "transfer initiate failed"
    fi
}

transfer_poll() {
    if [[ -z "$TRANSFER_CONSUMER_PID" ]]; then
        log_fail "no transfer started yet"
        return
    fi
    log_step "Polling own transfer state"
    local resp
    resp=$(curl -s -H "Host: ${VU_DSP_HOST}" -H "Authorization: Bearer ${DAT}" \
        "${INGRESS_URL}/api/v1/callback/transfers/${TRANSFER_CONSUMER_PID_ENC}")
    log_json "$resp"
    TRANSFER_STATE=$(echo "$resp" | jq -r '.state // empty')
    if [[ "$TRANSFER_STATE" == "COMPLETED" ]]; then
        log_ok "real data:"
        echo "$resp" | jq '.dataAddress'
    fi
}

transfer_menu() {
    while true; do
        echo
        echo "--- Transfer (state: ${TRANSFER_STATE:-none}) ---"
        if [[ -z "$TRANSFER_STATE" ]]; then
            echo "1) Initiate transfer (as Consumer)"
        else
            echo "(async - just poll until COMPLETED)"
        fi
        echo "p) Poll current status"
        echo "0) Back to main menu"
        read -rp "> " choice
        case "${TRANSFER_STATE}:${choice}" in
            ":1") transfer_initiate ;;
            *:p|*:P) transfer_poll ;;
            *:0) return ;;
            *) echo "  (not a valid choice for the current state)" ;;
        esac
    done
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
# cleanup_on_exit kills this run's own port-forwards (otherwise they linger
# as orphaned kubectl processes after the script exits). Does NOT touch the
# kind cluster container itself - an earlier version also did `docker stop`
# on it, which meant every menu exit (choice 0, or Ctrl-C) shut the whole
# cluster down, forcing a manual `docker start` before the next run. Live-
# found running this script for real: confusing back-to-back-session
# friction, not worth it - a script exit should not double as a cluster
# teardown.
cleanup_on_exit() {
    echo
    log_step "Shutting down port-forwards"
    pkill -9 -f "port-forward svc/nginx-nginx-ingress-controller ${INGRESS_PORT}:80" 2>/dev/null
    pkill -9 -f "port-forward svc/negotiation-service-vu ${NEG_VU_PORT}:8080" 2>/dev/null
    pkill -9 -f "port-forward svc/negotiation-service-uva ${NEG_UVA_PORT}:8080" 2>/dev/null
    pkill -9 -f "port-forward svc/etcd ${ETCD_PORT}:2379" 2>/dev/null
}

main() {
    require_tools
    health_check
    get_dat
    sync_party_dat
    health_check   # negotiation-service pods just rolled - re-check/restart their port-forwards
    seed_uva_relation
    seed_employee_survey_dataset

    # Registered here, not at the top of main() - an error exit during the
    # startup sequence above (get_dat, etc.) must NOT tear down the cluster.
    # Live-found running this script for real: a transient mint-identity
    # failure during startup triggered cleanup_on_exit, which docker-stopped
    # the still-healthy kind node mid-startup - turning a recoverable hiccup
    # into a full outage needing a manual `docker start` + kubeconfig
    # re-export to recover from. Only once the demo is actually up and
    # running does exiting (menu choice 0, or Ctrl-C) mean "I'm done, shut
    # it all down".
    trap cleanup_on_exit EXIT

    while true; do
        echo
        echo "============================================================"
        echo "DSP Consumer-role demo - VU consumes from UVA"
        echo "============================================================"
        echo "1) Request Catalog"
        echo "2) Negotiation"
        echo "3) Transfer"
        echo "h) Re-run health check"
        echo "0) Exit"
        read -rp "> " choice
        case "$choice" in
            1) do_catalog ;;
            2) negotiation_menu ;;
            3) transfer_menu ;;
            h|H) health_check ;;
            0) echo "bye"; exit 0 ;;
            *) echo "  (not a valid choice)" ;;
        esac
    done
}

main
