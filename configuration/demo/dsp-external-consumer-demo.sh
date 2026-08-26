#!/bin/bash
# Real, end-to-end demo for issue #94: an external, non-DYNAMOS DSP
# connector (Eclipse EDC's Minimum Viable Dataspace, playing Consumer)
# requests data from a real DYNAMOS dsp-connector (VU, playing Provider) -
# real DCP-compliant negotiation, real job execution, real EDR-based data
# pull. No fixtures, no fake peer - proves DYNAMOS interops with somebody
# else's implementation, not just its own (contrast with #93/#95's
# DYNAMOS-to-DYNAMOS demo, configuration/demo/dsp-transfer-demo.sh).
#
# Usage:
#   ../setup-demos.sh                 # run once (or again after a real
#                                      # teardown) - covers both this demo
#                                      # and dsp-transfer-demo.sh
#   ./dsp-external-consumer-demo.sh   # interactive demo menu
#
# This script has no setup step of its own any more - configuration/demo/
# setup-demos.sh is the only bootstrap, shared with dsp-transfer-demo.sh.
# Run it first; this script assumes everything it does is already in place
# (MVD's cluster+app+identity, DYNAMOS's own redeploy, VU's real STS/job-
# fallback/EDR config, the reciprocal MVD-side wiring, VU's Relation in
# DYNAMOS's own etcd-backed catalog access model).
#
# The demo run itself (no args) is an interactive menu, not a single
# automated script - the user triggers each real HTTP call by hand and
# sees its full raw response, same "state-driven submenu, log_json every
# response" shape dsp-transfer-demo.sh's own main() already uses for
# DYNAMOS-to-DYNAMOS. What IS automated here is only what has to be:
# the cross-cluster network bridges (kind clusters can't otherwise reach
# each other's ClusterIP services). Negotiation still skips the
# Offer/Accept round-trip straight to Agreement once REQUESTED - this is
# what real testing found actually works cleanly, see
# negotiationAgreementHandler's own transition table - and MVD's own
# AGREED -> VERIFIED auto-progression is a real thing the user polls to
# see, not a step this script fakes or hides.
#
# No set -e: one failed step should still let later diagnostics run.
set -uo pipefail

SCRATCH_DIR="${TMPDIR:-/tmp}/dsp-external-consumer-demo"
mkdir -p "${SCRATCH_DIR}"

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
MVD_MGMT_PORT=18081       # MVD consumer controlplane Management API
MVD_DSP_BRIDGE_PORT=8082  # MVD consumer controlplane DSP protocol port, bridged for DYNAMOS's outbound pushes
MVD_STS_BRIDGE_PORT=7084  # MVD provider identityhub STS token endpoint, bridged for DYNAMOS's outbound DCP tokens
MVD_DID_BRIDGE_PORT=7083  # MVD consumer identityhub's did:web-serving port, bridged so DYNAMOS can verify MVD's real inbound DAT (dsp-connector-vu's own hostAlias points identityhub.consumer.svc.cluster.local at this bridge)
NEG_VU_PORT=18092         # negotiation-service-vu internal API
TPS_VU_PORT=18093         # transfer-process-service-vu internal API

VU_DSP_ADDRESS="http://dsp-vu.dsp-connector.svc.cluster.local:31464/api/v1"
VU_DID="did:web:identityhub.provider.svc.cluster.local%3A7083:vu"
MVD_CONSUMER_DID="did:web:identityhub.consumer.svc.cluster.local%3A7083:consumer"
MVD_API_KEY="password"

DATASET_ID="urn:dynamos:dataset:VU:wageGap"

# ---------------------------------------------------------------------------
# Output helpers - same shape as dsp-transfer-demo.sh's own
# ---------------------------------------------------------------------------
log_step() { echo; echo "=== $* ==="; }
log_ok()   { echo "  [ok] $*"; }
log_info() { echo "  [..] $*"; }
log_fail() { echo "  [FAIL] $*"; }
log_json() { echo "$1" | jq . 2>/dev/null || echo "$1"; }

# ensure_tools installs jq and netcat-openbsd via apt if missing - not in
# the devcontainer's own Dockerfile (live-found running this script fresh
# after a devcontainer rebuild). jq is used throughout for JSON handling;
# nc -z is used by ensure_bridges to poll a port-forward's readiness. Same
# self-healing pattern as dsp-transfer-demo.sh's own ensure_jq.
ensure_tools() {
    local missing=()
    command -v jq >/dev/null 2>&1 || missing+=(jq)
    command -v nc >/dev/null 2>&1 || missing+=(netcat-openbsd)
    if [[ ${#missing[@]} -eq 0 ]]; then
        return
    fi
    log_step "Installing missing tools: ${missing[*]}"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq >"${SCRATCH_DIR}/tools-install.log" 2>&1 \
            && apt-get install -y -qq "${missing[@]}" >>"${SCRATCH_DIR}/tools-install.log" 2>&1
    fi
    for tool in jq nc; do
        if ! command -v "$tool" >/dev/null 2>&1; then
            log_fail "could not install ${tool} automatically - see ${SCRATCH_DIR}/tools-install.log"
            log_fail "install it yourself (apt-get install jq netcat-openbsd) and re-run"
            exit 1
        fi
    done
    log_ok "tools installed"
}

# ---------------------------------------------------------------------------
# kind_node_ip is still needed at demo-run time (pull_real_result dials
# DYNAMOS's real NodePort service directly via the node's own container
# IP) - everything else setup-only (kind_gateway_ip, ensure_host_alias,
# ensure_env_var, ensure_env_from_ref, setup_mvd_cluster, setup_mvd,
# setup_dynamos, mint_vu_participant, delete_vu_participant) moved to
# configuration/demo/setup-demos.sh, run once, separately from this script.
kind_node_ip() {
    docker inspect "$1" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null
}


# ---------------------------------------------------------------------------
# Cross-cluster bridges - kind-mvd and kind-dynamos can't reach each
# other's ClusterIP services directly (confirmed live, see
# wiki/devops/mvd-demo-dataspace-setup.md section 6), so every direction
# needed goes through a host-bound kubectl port-forward plus a hostAlias
# on the receiving pod pointing the DNS name at the docker "kind" network's
# gateway IP (172.20.0.1, host-reachable from any container on that
# network). The hostAlias patches themselves are one-time deployment
# changes, already applied - this only (re)starts the port-forward
# processes, which don't survive a shell restart.
# ---------------------------------------------------------------------------
ensure_bridge() {
    local name="$1" context="$2" ns="$3" svc="$4" local_port="$5" remote_port="$6" bind_addr="$7"
    if curl -sf --max-time 2 "http://localhost:${local_port}/" >/dev/null 2>&1; then
        log_ok "localhost:${local_port} -> ${context}/${ns}/${svc} (${name})"
        return 0
    fi
    pkill -9 -f "port-forward.*${ns} svc/${svc} ${local_port}:${remote_port}" 2>/dev/null
    sleep 1
    log_info "starting bridge: ${name} (localhost:${local_port} -> ${context}/${ns}/${svc}:${remote_port})"
    setsid kubectl --context "$context" -n "$ns" port-forward "svc/${svc}" "${local_port}:${remote_port}" --address "$bind_addr" \
        > "${SCRATCH_DIR}/pf-${name}.log" 2>&1 < /dev/null &
    disown
    for _ in $(seq 1 10); do
        sleep 1
        if nc -z localhost "${local_port}" 2>/dev/null; then
            log_ok "bridge ${name} up"
            return 0
        fi
    done
    log_fail "bridge ${name} did not come up (see ${SCRATCH_DIR}/pf-${name}.log)"
    return 1
}

ensure_bridges() {
    log_step "Cross-cluster bridges"
    local all_ok=true
    ensure_bridge mvd-mgmt   kind-mvd     consumer controlplane "$MVD_MGMT_PORT" 8081 127.0.0.1 || all_ok=false
    ensure_bridge mvd-dsp    kind-mvd     consumer controlplane "$MVD_DSP_BRIDGE_PORT" 8082 0.0.0.0 || all_ok=false
    ensure_bridge mvd-sts    kind-mvd     provider identityhub  "$MVD_STS_BRIDGE_PORT" 7084 0.0.0.0 || all_ok=false
    ensure_bridge mvd-did    kind-mvd     consumer identityhub  "$MVD_DID_BRIDGE_PORT" 7083 0.0.0.0 || all_ok=false
    ensure_bridge neg-vu     kind-dynamos negotiation-service negotiation-service-vu "$NEG_VU_PORT" 8080 127.0.0.1 || all_ok=false
    ensure_bridge tps-vu     kind-dynamos transfer-process-service transfer-process-service-vu "$TPS_VU_PORT" 8080 127.0.0.1 || all_ok=false
    $all_ok
}

mgmt_post() {
    curl -sS -m 15 -H "X-Api-Key: ${MVD_API_KEY}" -H 'Content-Type: application/json' -X POST "http://127.0.0.1:${MVD_MGMT_PORT}$1" -d "$2"
}
mgmt_get() {
    curl -sS -m 10 -H "X-Api-Key: ${MVD_API_KEY}" "http://127.0.0.1:${MVD_MGMT_PORT}$1"
}
internal_post() {
    curl -sS -m 30 -H 'Content-Type: application/json' -X POST "$1" -d "$2"
}

# ---------------------------------------------------------------------------
# State - persists across menu choices within this run. Same interactive,
# state-driven-submenu shape as dsp-transfer-demo.sh's own main() (issue
# #93/#95's own demo): every real HTTP call the user triggers by hand
# prints its full raw response via log_json, and steps MVD or DYNAMOS
# handle automatically (auto-verify, the real job execution) are called out
# as "poll to see it" rather than being silently skipped past, issue #94.
# ---------------------------------------------------------------------------
OFFER_ID=""
MVD_NEG_ID=""
PROVIDER_PID=""
MVD_STATE=""
CONTRACT_AGREEMENT_ID=""
TRANSFER_ID=""
TRANSFER_STATE=""
TRANSFER_PROVIDER_PID=""

# ---------------------------------------------------------------------------
# 1. Real catalog request - MVD's real controlplane to DYNAMOS's real
#    dsp-connector-vu. The offer id is re-derived live every run: it
#    rotates to the finalizing negotiation's own providerPid each time a
#    negotiation reaches FINALIZED (policy-enforcer's own doing, see
#    negotiation-service's applyPolicyEnforcement) - hardcoding it would
#    only work once.
# ---------------------------------------------------------------------------
do_catalog() {
    log_step "Requesting DYNAMOS's (VU's) catalog, as MVD/Consumer"
    local resp
    resp=$(mgmt_post /api/mgmt/v4/catalog/request "$(jq -n --arg addr "$VU_DSP_ADDRESS" '{
        "@context": ["https://w3id.org/edc/connector/management/v2"],
        "@type": "CatalogRequest",
        "counterPartyAddress": $addr,
        "counterPartyId": "urn:dynamos:party:VU",
        "protocol": "dataspace-protocol-http:2025-1",
        "querySpec": {"offset": 0, "limit": 50}
    }')")
    log_json "$resp"
    OFFER_ID=$(echo "$resp" | jq -r --arg d "$DATASET_ID" '.dataset[]? | select(.["@id"]==$d) | .hasPolicy[0]["@id"] // empty')
    if [[ -z "$OFFER_ID" ]]; then
        log_fail "no offer id found in catalog response"
    else
        log_ok "offer id: ${OFFER_ID}"
    fi
}

# ---------------------------------------------------------------------------
# 2. Negotiation - state-machine-driven submenu. Provider-role Offer/Accept
#    round-trip is skipped deliberately: DYNAMOS's own negotiation-service
#    allows AGREED directly from REQUESTED (negotiationAgreementHandler's
#    own transition table), which is also what a real, decisive Provider
#    that agrees outright does - re-sending the exact same Offer back adds
#    a round trip real EDC only auto-progresses if the offer is a byte-
#    identical ODRL policy, live-found fragile. MVD auto-drives
#    AGREED -> VERIFIED on its own once a valid, timestamped Agreement
#    lands - confirmed live, poll to see it rather than a separate step.
# ---------------------------------------------------------------------------
neg_initiate() {
    if [[ -z "$OFFER_ID" ]]; then
        log_fail "no offer id yet - run 'Request Catalog' (main menu 1) first"
        return
    fi
    log_step "Initiating negotiation as MVD/Consumer (real Management API call)"
    local resp
    resp=$(mgmt_post /api/mgmt/v4/contractnegotiations "$(jq -n --arg offer "$OFFER_ID" --arg vu "$VU_DID" --arg addr "$VU_DSP_ADDRESS" '{
        "@context": ["https://w3id.org/edc/connector/management/v2"],
        "@type": "ContractRequest",
        "counterPartyAddress": $addr,
        "counterPartyId": $vu,
        "protocol": "dataspace-protocol-http:2025-1",
        "policy": {
            "@type": "Offer", "@id": $offer, "assigner": $vu,
            "target": "urn:dynamos:dataset:VU:wageGap",
            "permission": [{"action":"dynamos:sqlDataRequest","constraint":[
                {"leftOperand":"dynamos:archetype","operator":"isAnyOf","rightOperand":["computeToData","dataThroughTtp"]},
                {"leftOperand":"dynamos:computeProvider","operator":"isAnyOf","rightOperand":["SURF"]}
            ]}]
        },
        "callbackAddresses": []
    }')")
    log_json "$resp"
    MVD_NEG_ID=$(echo "$resp" | jq -r '.["@id"] // empty')
    if [[ -z "$MVD_NEG_ID" ]]; then
        log_fail "negotiation initiate failed"
        return
    fi
    PROVIDER_PID=""
    MVD_STATE="REQUESTED"
    log_ok "MVD negotiation ${MVD_NEG_ID} started"
    log_info "poll to see DYNAMOS's own providerPid (correlationId) appear"
}

neg_poll() {
    if [[ -z "$MVD_NEG_ID" ]]; then
        log_fail "no negotiation started yet"
        return
    fi
    log_step "Polling MVD's own negotiation state"
    local resp
    resp=$(mgmt_get "/api/mgmt/v4/contractnegotiations/${MVD_NEG_ID}")
    log_json "$resp"
    MVD_STATE=$(echo "$resp" | jq -r '.state // empty')
    if [[ -z "$PROVIDER_PID" ]]; then
        PROVIDER_PID=$(echo "$resp" | jq -r '.correlationId // empty')
        [[ -n "$PROVIDER_PID" ]] && log_ok "DYNAMOS providerPid: ${PROVIDER_PID}"
    fi
    CONTRACT_AGREEMENT_ID=$(echo "$resp" | jq -r '.contractAgreementId // empty')
}

neg_send_agreement() {
    if [[ -z "$PROVIDER_PID" ]]; then
        log_fail "no providerPid yet - poll until it appears"
        return
    fi
    log_step "Sending Agreement (playing VU/Provider side, DYNAMOS's own internal API)"
    local enc ts resp
    enc=$(jq -rn --arg p "$PROVIDER_PID" '$p|@uri')
    ts=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
    resp=$(internal_post "http://127.0.0.1:${NEG_VU_PORT}/internal/v1/negotiations/${enc}/agreement" \
        "$(jq -n --arg id "urn:dynamos:agreement:VU:demo-$(date +%s)" --arg vu "$VU_DID" --arg mvd "did:web:identityhub.consumer.svc.cluster.local%3A7083:consumer" --arg ts "$ts" '{
        "agreement": {
            "@type": "Agreement", "@id": $id, "target": "urn:dynamos:dataset:VU:wageGap",
            "assigner": $vu, "assignee": $mvd, "timestamp": $ts,
            "permission": [{"action":"dynamos:sqlDataRequest","constraint":[
                {"leftOperand":"dynamos:archetype","operator":"isAnyOf","rightOperand":["computeToData","dataThroughTtp"]},
                {"leftOperand":"dynamos:computeProvider","operator":"isAnyOf","rightOperand":["SURF"]}
            ]}]
        }
    }')")
    log_json "$resp"
    if echo "$resp" | jq -e '.state=="AGREED"' >/dev/null 2>&1; then
        log_ok "DYNAMOS side: AGREED"
        log_info "MVD auto-verifies within ~1-3s - poll to see it"
    else
        log_fail "provider agreement failed"
    fi
}

neg_send_finalize() {
    if [[ -z "$PROVIDER_PID" ]]; then
        log_fail "no providerPid"
        return
    fi
    log_step "Sending FINALIZED event (playing VU/Provider side)"
    local enc resp
    enc=$(jq -rn --arg p "$PROVIDER_PID" '$p|@uri')
    resp=$(internal_post "http://127.0.0.1:${NEG_VU_PORT}/internal/v1/negotiations/${enc}/events" '{"eventType":"FINALIZED"}')
    log_json "$resp"
    if echo "$resp" | jq -e '.state=="FINALIZED"' >/dev/null 2>&1; then
        log_ok "DYNAMOS side: FINALIZED"
        log_info "poll to see MVD's own contractAgreementId appear"
    else
        log_fail "provider FINALIZED failed"
    fi
}

negotiation_menu() {
    while true; do
        echo
        echo "--- Negotiation (MVD state: ${MVD_STATE:-none}) ---"
        case "$MVD_STATE" in
            "")
                echo "1) Initiate negotiation (as MVD/Consumer, real Management API)"
                ;;
            REQUESTED)
                if [[ -n "$PROVIDER_PID" ]]; then
                    echo "1) Send Agreement (playing VU/Provider side)"
                else
                    echo "(waiting for DYNAMOS's own providerPid - poll to check)"
                fi
                ;;
            AGREED)
                echo "(waiting on MVD's own auto-verify - poll to check)"
                ;;
            VERIFIED)
                echo "1) Send FINALIZED event (playing VU/Provider side)"
                ;;
            FINALIZED)
                echo "FINALIZED - ready for the Transfer menu (main menu 3)"
                ;;
        esac
        echo "p) Poll MVD's own negotiation status"
        echo "0) Back to main menu"
        read -rp "> " choice
        case "${MVD_STATE}:${choice}" in
            ":1") neg_initiate ;;
            "REQUESTED:1") neg_send_agreement ;;
            "VERIFIED:1") neg_send_finalize ;;
            *:p|*:P) neg_poll ;;
            *:0) return ;;
            *) echo "  (not a valid choice for the current state)" ;;
        esac
    done
}

# ---------------------------------------------------------------------------
# 3. Transfer - state-machine-driven submenu. MVD pulls real data via a
#    real EDR (transfer-process-service's own wireDataAddress, issue #94).
# ---------------------------------------------------------------------------
# Unlike dsp-transfer-demo.sh's own choose_data_address (DYNAMOS-to-DYNAMOS,
# talks to negotiation-service's own internal API directly), a real
# external DSP consumer here cannot choose its own query. Tried it live,
# issue #94: transfer-process-service's own triggerJobAndDeliver
# (job_execution.go) only remaps Format to "dynamos:sqlDataRequest" - the
# only route vu's own agent actually serves - when the incoming
# DataAddress is completely empty. Supplying ANY dataDestination content
# (a custom query, or even the operator's own default re-sent explicitly)
# makes DataAddress non-empty, so Format stays "HttpData-PULL" (the real
# DSP transferType) instead - vu's agent has no such route, 404, transfer
# TERMINATED. This is deliberate on DYNAMOS's side (see that function's own
# long comment: TCK TP:03-01/03-02 negative-test preservation),
# not a bug this script can work around - a real fix would mean deciding
# to decouple Format-remapping from the DataAddress-empty check, a
# TCK-compliance tradeoff worth a real conversation, not a silent change
# here. So: the only two states MVD_STATE this deployment can actually
# produce are "empty dataDestination, DYNAMOS's own configured default
# query runs" (defaultJobType/defaultJobRequest, both set for this demo)
# and "custom content, terminated" - there is no working job-less state
# either once those are set, same reason.
transfer_initiate() {
    if [[ "$MVD_STATE" != "FINALIZED" || -z "$CONTRACT_AGREEMENT_ID" ]]; then
        log_fail "negotiation is not FINALIZED yet (state: ${MVD_STATE:-none}) - finish the Negotiation menu first"
        return
    fi
    log_step "Initiating transfer as MVD/Consumer (real Management API call)"
    local resp
    resp=$(mgmt_post /api/mgmt/v4/transferprocesses "$(jq -n --arg addr "$VU_DSP_ADDRESS" --arg vu "$VU_DID" --arg contract "$CONTRACT_AGREEMENT_ID" --arg asset "$DATASET_ID" '{
        "@context": ["https://w3id.org/edc/connector/management/v2"],
        "@type": "TransferRequest",
        "assetId": $asset,
        "counterPartyAddress": $addr,
        "connectorId": $vu,
        "contractId": $contract,
        "dataDestination": {"@type": "DataAddress", "type": "HttpProxy"},
        "protocol": "dataspace-protocol-http:2025-1",
        "transferType": "HttpData-PULL"
    }')")
    log_json "$resp"
    TRANSFER_ID=$(echo "$resp" | jq -r '.["@id"] // empty')
    if [[ -z "$TRANSFER_ID" ]]; then
        log_fail "transfer initiate failed"
        return
    fi
    TRANSFER_STATE="REQUESTED"
    log_ok "transfer ${TRANSFER_ID} requested"
    log_info "DYNAMOS runs its own configured default job automatically - poll for COMPLETED"
}

# fetch_mvd_consumer_sts_token mints a real STS access token as MVD's own
# consumer participant (OAuth2 client_credentials, real Vault-stored
# secret - not fabricated) - the same real credential MVD's own dataplane
# would use to pull the actual result, if its own bug didn't prevent it
# from ever issuing that call. Runs via a throwaway pod inside kind-mvd's
# own consumer namespace: consumer's own identityhub STS port (7084) has
# no host-reachable bridge (only provider's does, for VU's own outbound
# calls) - cluster-internal DNS is the only path.
fetch_mvd_consumer_sts_token() {
    local secret
    secret=$(kubectl --context kind-mvd exec -n consumer deploy/vault -- sh -c \
        "VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root vault kv get -field=content secret/consumer-participant-sts-client-secret" 2>/dev/null)
    if [[ -z "$secret" ]]; then
        return 1
    fi
    # --data-urlencode, not a raw -d "client_id=$CLIENT_ID": the DID's own
    # did:web escaping already contains a literal "%3A" (the host:port
    # colon, per the did:web spec). A raw -d sends that "%3A" unencoded, so
    # the server's own form-decode turns it into a real ":" - a client_id
    # that no longer matches what's actually stored, "invalid_client".
    # --data-urlencode encodes the value's own "%" to "%25" so the literal
    # "%3A" round-trips correctly through exactly one decode. Same issue
    # for audience (VU_DID, same escaping). Live-found running this for
    # real, issue #94.
    local pod_script='
curl -sS -X POST "http://identityhub.consumer.svc.cluster.local:7084/api/sts/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    --data-urlencode "client_id=$CLIENT_ID" \
    --data-urlencode "client_secret=$CLIENT_SECRET" \
    --data-urlencode "audience=$AUD"
'
    kubectl --context kind-mvd delete pod mint-consumer-token -n consumer --ignore-not-found >/dev/null 2>&1
    kubectl --context kind-mvd run mint-consumer-token -n consumer --restart=Never --quiet \
        --image=curlimages/curl:latest \
        --env="CLIENT_ID=${MVD_CONSUMER_DID}" --env="CLIENT_SECRET=${secret}" --env="AUD=${VU_DID}" \
        -- sh -c "$pod_script" >/dev/null 2>&1
    if ! kubectl --context kind-mvd wait --for=jsonpath='{.status.phase}'=Succeeded pod/mint-consumer-token -n consumer --timeout=30s >/dev/null 2>&1; then
        kubectl --context kind-mvd delete pod mint-consumer-token -n consumer --ignore-not-found >/dev/null 2>&1
        return 1
    fi
    local resp
    resp=$(kubectl --context kind-mvd logs mint-consumer-token -n consumer 2>/dev/null)
    kubectl --context kind-mvd delete pod mint-consumer-token -n consumer --ignore-not-found >/dev/null 2>&1
    echo "$resp" | jq -r '.access_token // empty'
}

# pull_real_result does the real HTTP pull GET MVD's own dataplane should
# issue but never does (its own bug, "No dataplane found" - see
# wiki/devops/mvd-demo-dataspace-setup.md) - same endpoint
# (transfer_result_handler.go), same real DAT auth, same real data DYNAMOS
# already generated and is sitting there waiting to serve. Not a
# DYNAMOS-side gap: this proves the provider side works end to end,
# live-verified, issue #94.
pull_real_result() {
    local token dynamos_ip resp
    token=$(fetch_mvd_consumer_sts_token)
    if [[ -z "$token" ]]; then
        log_fail "could not mint a real STS token as MVD's consumer"
        return
    fi
    dynamos_ip=$(kind_node_ip dynamos-control-plane)
    if [[ -z "$dynamos_ip" ]]; then
        log_fail "could not determine kind-dynamos node's own container IP"
        return
    fi
    resp=$(curl -sS -m 15 -H "Authorization: Bearer ${token}" \
        "http://${dynamos_ip}:31464/api/v1/transfers/${TRANSFER_PROVIDER_PID}/result")
    log_json "$resp"
}

transfer_poll() {
    if [[ -z "$TRANSFER_ID" ]]; then
        log_fail "no transfer started yet"
        return
    fi
    log_step "Polling MVD's own transfer state"
    local resp
    resp=$(mgmt_get "/api/mgmt/v4/transferprocesses/${TRANSFER_ID}")
    log_json "$resp"
    TRANSFER_STATE=$(echo "$resp" | jq -r '.state // empty')
    TRANSFER_PROVIDER_PID=$(echo "$resp" | jq -r '.correlationId // empty')
    if [[ "$TRANSFER_STATE" == "COMPLETED" ]]; then
        if [[ -n "$TRANSFER_PROVIDER_PID" ]]; then
            pull_real_result
        fi
    fi
}

transfer_menu() {
    while true; do
        echo
        echo "--- Transfer (state: ${TRANSFER_STATE:-none}) ---"
        if [[ -z "$TRANSFER_STATE" ]]; then
            echo "1) Initiate transfer (as MVD/Consumer, real Management API)"
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
main() {
    ensure_tools
    ensure_bridges || { log_fail "bridges not all up, aborting"; exit 1; }

    while true; do
        echo
        echo "============================================================"
        echo "External DSP consumer demo - MVD consumes from DYNAMOS (VU)"
        echo "============================================================"
        echo "1) Request Catalog"
        echo "2) Negotiation"
        echo "3) Transfer"
        echo "b) Re-check cross-cluster bridges"
        echo "0) Exit"
        read -rp "> " choice
        case "$choice" in
            1) do_catalog ;;
            2) negotiation_menu ;;
            3) transfer_menu ;;
            b|B) ensure_bridges ;;
            0) echo "bye"; exit 0 ;;
            *) echo "  (not a valid choice)" ;;
        esac
    done
}

main "$@"
