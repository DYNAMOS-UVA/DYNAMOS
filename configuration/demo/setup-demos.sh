#!/bin/bash
# One-time (or "run again after a real teardown") bootstrap for BOTH demo
# scripts in this directory:
#   - dsp-transfer-demo.sh (issue #93/#97, DYNAMOS-to-DYNAMOS)
#   - dsp-external-consumer-demo.sh (issue #94, real external MVD consumer)
#
# Run this once, then run either demo script directly - neither demo script
# has its own --setup flag any more; this is the only setup step. Safe to
# re-run any time (idempotent, checks real state before acting), and is
# also the right thing to run again after a genuine `kind delete cluster`
# for either kind-dynamos or kind-mvd.
#
# Usage:
#   ./setup-demos.sh
#
# Covers, in order (each step checks real state first, so a rerun on an
# already-correct machine is a fast no-op):
#   1. ensure_tools        - jq, netcat-openbsd (devcontainer gap, not in
#                             its own Dockerfile)
#   2. setup_mvd_cluster   - kind-mvd node, Traefik, Gateway API CRDs,
#                             MVD's own app images (gradlew dockerize from
#                             source if not already built - the slow step,
#                             real wall-clock minutes), image load, app
#                             manifests (kubectl apply -k k8s)
#   3. setup_mvd           - MVD's own identity data (vault-bootstrap x3,
#                             issuerservice-seed, identityhub-seed, and
#                             VU's own real participant context on
#                             provider's IdentityHub)
#   4. setup_dynamos       - kind-dynamos node, dynamos-configuration.sh
#                             (redeploys every DYNAMOS chart), VU's real
#                             STS/job-fallback/EDR config + hostAliases +
#                             the reciprocal MVD-side wiring + VU's
#                             Relation in DYNAMOS's own etcd-backed catalog
#                             access model - everything issue #94's own
#                             external-consumer demo needs
#   5. setup_internal_demo - everything issue #93/#97's own DYNAMOS-to-
#                             DYNAMOS demo additionally needs: fixture-did
#                             Deployment+Service, agent image tags
#                             (dynamos1/agent:dsp-latest - issue #97's
#                             job-name fix isn't merged to main yet),
#                             SQL-QUERY_TAG (employeeSurvey dataset CSVs),
#                             dsp-connector-vu's own CONNECTOR_BASE_URL,
#                             dsp-connector-uva's own DID_WEB_SCHEME
#
# Steps 2-4 run in that order deliberately: setup_mvd may mint a fresh
# VU_STS_CLIENT_SECRET, which setup_dynamos needs already in hand before it
# writes DYNAMOS's own k8s Secret.
#
# No set -e: one failed step should still let later diagnostics run.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
MVD_ROOT="${REPO_ROOT}/../dynamos-mvd"
SCRATCH_DIR="${TMPDIR:-/tmp}/dsp-demos-setup"
mkdir -p "${SCRATCH_DIR}"

# VU's real MVD-issued identity (provider's IdentityHub). This starting
# value is only a fallback for a first-ever run - setup_mvd itself always
# re-derives the real value from provider's IdentityHub DB (mvd_psql's own
# vu_count check), and overwrites this variable with a freshly minted
# secret (mint_vu_participant) whenever MVD's identity data was wiped -
# live-found: MVD's own Postgres/Vault run with no PVC (ephemeral storage),
# so any restart of the mvd-control-plane node container wipes every
# participant context, VU's included, issue #94.
VU_STS_CLIENT_SECRET="7GiLm5q55aRs4UP7"
# algorithm "average" (not "raw") - sql-algorithm's own dispatch
# (application_logic.go) only aggregates on the exact string "average",
# hardcoded to wageGap's own column names (Geslacht/Aanst_22/Gebdat) -
# anything else is a plain pass-through of every raw joined row. This is
# what actually computes the wage gap (average salary by gender), not
# just the raw source rows.
DEFAULT_JOB_REQUEST_JSON='{"query":"SELECT * FROM Personen p JOIN Aanstellingen s LIMIT 50","algorithm":"average","algorithmColumns":{"Geslacht":"Aanst_22, Gebdat"}}'

VU_DSP_ADDRESS="http://dsp-vu.dsp-connector.svc.cluster.local:31464/api/v1"
VU_DID="did:web:identityhub.provider.svc.cluster.local%3A7083:vu"
MVD_CONSUMER_DID="did:web:identityhub.consumer.svc.cluster.local%3A7083:consumer"

# agent's own chart still defaults to :main, which does not have issue
# #97's job-name-length fix yet (not merged to main) - dsp-latest is the
# real Docker Hub tag this branch's own fixed build was pushed under.
AGENT_IMAGE_TAG="dsp-latest"
# dynamos1/sql-query:main has no data for the employeeSurvey dataset (its
# CSVs were only added to this branch's own build) - dsp-latest carries them.
SQL_QUERY_TAG="dsp-latest"

# ---------------------------------------------------------------------------
# Output helpers - same shape both demo scripts already use
# ---------------------------------------------------------------------------
log_step() { echo; echo "=== $* ==="; }
log_ok()   { echo "  [ok] $*"; }
log_info() { echo "  [..] $*"; }
log_fail() { echo "  [FAIL] $*"; }

# ensure_tools installs jq and netcat-openbsd via apt if missing - not in
# the devcontainer's own Dockerfile. jq is used throughout for JSON
# handling; nc -z is used by the demo scripts' own bridge/port-forward
# readiness checks.
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

# ensure_coredns reinstalls the coredns addon (via kubeadm, the same
# mechanism kind itself uses at cluster bootstrap) if it's missing from
# kube-system - it never is on a normal kind cluster, but `kubectl delete
# deployments --all -A` (a real teardown mode this session used) deletes
# it same as everything else, and neither this script nor
# dynamos-configuration.sh manages it - it's a cluster-infra addon, not
# part of any app manifest. Every service-to-service DNS lookup in the
# whole cluster silently breaks without it (vault-bootstrap, issuerservice,
# every "*.svc.cluster.local" address this script itself relies on) - a
# much wider blast radius than any single missing app Deployment. Live-
# found running this for real, issue #94.
ensure_coredns() {
    local ctx="$1" node="$2"
    if kubectl --context "$ctx" -n kube-system get deployment coredns >/dev/null 2>&1; then
        return
    fi
    log_info "coredns missing in ${ctx} - reinstalling via kubeadm..."
    docker exec "$node" kubeadm init phase addon coredns --kubeconfig /etc/kubernetes/admin.conf >/dev/null 2>&1
    kubectl --context "$ctx" -n kube-system rollout status deployment/coredns --timeout=90s >/dev/null \
        || { log_fail "coredns did not come up in ${ctx} after reinstalling"; return 1; }
    log_ok "coredns reinstalled in ${ctx}"
}

# ---------------------------------------------------------------------------
kind_node_ip() {
    docker inspect "$1" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null
}

# kind_gateway_ip is the docker "kind" bridge network's own gateway - the
# actual HOST machine's address as seen from any container on that network
# (e.g. 172.20.0.1). This is what a pod must dial to reach a kubectl
# port-forward process, which runs on the host itself, not inside either
# kind node's own container - dialing the other kind node's container IP
# directly would reach that container's own network namespace instead,
# where nothing is listening on the port-forward's port. Live-found: this
# exact mix-up broke dsp-connector-vu's DAT verification after a fresh
# setup run, issue #94.
kind_gateway_ip() {
    docker network inspect kind --format '{{json .IPAM.Config}}' 2>/dev/null | jq -r '.[] | select(.Subnet | contains(":") | not) | .Gateway'
}

# ensure_host_alias replaces (not appends) hostAliases wholesale - safe to
# call every run, kubectl no-ops a patch that doesn't actually change the
# resulting spec, so no needless rollout on an already-correct deployment.
ensure_host_alias() {
    local context="$1" ns="$2" deploy="$3" json="$4"
    kubectl --context "$context" patch deployment "$deploy" -n "$ns" --type='json' \
        -p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/hostAliases\",\"value\":${json}}]" \
        >/dev/null 2>&1 || \
    kubectl --context "$context" patch deployment "$deploy" -n "$ns" --type='json' \
        -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/hostAliases\",\"value\":${json}}]" \
        >/dev/null
}

# ensure_env_var sets a plain-value env var - kubectl set env is already
# idempotent (no-op / no rollout if the value is unchanged).
ensure_env_var() {
    kubectl --context "$1" -n "$2" set env "deployment/$3" "$4=$5" >/dev/null
}

# ensure_env_from_ref adds a secretKeyRef/configMapKeyRef-backed env var
# only if it isn't already present - unlike a plain value, `kubectl set
# env` can't express a ref, so this uses a JSON patch, which would
# otherwise duplicate the env entry on every rerun.
ensure_env_from_ref() {
    local context="$1" ns="$2" deploy="$3" name="$4" ref_json="$5"
    if kubectl --context "$context" -n "$ns" get deployment "$deploy" -o json 2>/dev/null \
        | jq -e --arg n "$name" '.spec.template.spec.containers[0].env[]? | select(.name==$n)' >/dev/null; then
        return 0
    fi
    kubectl --context "$context" -n "$ns" patch deployment "$deploy" --type='json' \
        -p="[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/env/-\",\"value\":{\"name\":\"${name}\",\"valueFrom\":${ref_json}}}]" \
        >/dev/null
}

setup_dynamos() {
    log_step "Setup: DYNAMOS (kind-dynamos)"

    if ! docker ps --filter name=dynamos-control-plane --filter status=running -q | grep -q .; then
        log_info "dynamos-control-plane not running, starting it..."
        docker start dynamos-control-plane >/dev/null
        sleep 10
        # Same real port drift as kind-mvd's own restart path
        # (setup_mvd_cluster) - the API server's host port isn't
        # guaranteed to survive a stop, and kubeconfig can end up
        # pointing at a stale one. Re-export unconditionally, no-op if
        # nothing changed. Live-found running this for real, issue #94.
        kind export kubeconfig --name dynamos >/dev/null 2>&1
    fi
    log_ok "kind-dynamos node running"
    ensure_coredns kind-dynamos dynamos-control-plane || return 1

    # A `docker stop`/`start` cycle on this node recreates every pod's
    # sandbox ("Pod sandbox changed, it will be killed and re-created" in
    # `kubectl describe pod`) - linkerd-proxy's own postStart hook
    # sometimes fails against the fresh sandbox's iptables state and the
    # container CrashLoopBackOffs forever on its own (etcd's own 3 pods,
    # every restart, live-found this session). The pod's app container
    # (etcd itself) keeps running underneath, but the pod stays NotReady,
    # so headless-Service DNS drops it and orchestrator's etcd writes
    # fail with "no such host". A plain pod delete forces a clean sandbox
    # + a fresh linkerd-init run, which always clears it. Issue #94.
    local stale
    stale=$(kubectl --context kind-dynamos get pods -A -o json 2>/dev/null | jq -r '
        .items[] | select(.status.containerStatuses[]? |
            select(.name=="linkerd-proxy" and (.state.waiting.reason // "")=="CrashLoopBackOff")) |
        "\(.metadata.namespace)/\(.metadata.name)"')
    if [[ -n "$stale" ]]; then
        log_info "stale linkerd-proxy sidecar(s) stuck from an earlier sandbox recreate - deleting for a clean respawn..."
        local pod
        while read -r pod; do
            [[ -z "$pod" ]] && continue
            kubectl --context kind-dynamos -n "${pod%%/*}" delete pod "${pod##*/}" --ignore-not-found >/dev/null 2>&1
        done <<< "$stale"
        kubectl --context kind-dynamos -n core rollout status statefulset/etcd --timeout=180s >/dev/null || return 1
        log_ok "stale sidecar(s) cleared"
    fi

    log_info "running dynamos-configuration.sh (idempotent helm upgrade -i for every chart)..."
    # DYNAMOS_HOST_ROOT must be the real HOST filesystem path (used for the
    # "core" chart's own hostPath volume, which orchestrator reads
    # etcd_launch_files/*.json from on every start to seed etcd's baseline
    # agreements/datasets - see etcd_config.go's registerPolicyEnforcerConfiguration).
    # kind-dynamos's own node container has no idea what "/workspace" is -
    # that mount point only exists inside this devcontainer's own
    # namespace - so unconditionally overriding it here silently broke
    # orchestrator's own etcd seeding on every setup run from inside the
    # devcontainer, invisible until a genuine `kind delete cluster` wiped
    # etcd's real PVC data for the first time, issue #94. Only fall back to
    # REPO_ROOT (correct when this script runs directly on the host) if
    # DYNAMOS_HOST_ROOT isn't already set - dev.sh's own -e flag (from
    # .env) is what sets it correctly inside the devcontainer.
    if ! ( cd "${REPO_ROOT}" && DYNAMOS_HOST_ROOT="${DYNAMOS_HOST_ROOT:-${REPO_ROOT}}" bash configuration/dynamos-configuration.sh > "${SCRATCH_DIR}/dynamos-configuration.log" 2>&1 ); then
        log_fail "dynamos-configuration.sh failed, see ${SCRATCH_DIR}/dynamos-configuration.log"
        return 1
    fi
    log_ok "dynamos-configuration.sh complete"

    log_info "waiting for VU's DSP services to be ready..."
    kubectl --context kind-dynamos -n dsp-connector rollout status deployment/dsp-connector-vu --timeout=180s >/dev/null || return 1
    kubectl --context kind-dynamos -n negotiation-service rollout status deployment/negotiation-service-vu --timeout=180s >/dev/null || return 1
    kubectl --context kind-dynamos -n transfer-process-service rollout status deployment/transfer-process-service-vu --timeout=180s >/dev/null || return 1
    log_ok "VU's dsp-connector/negotiation-service/transfer-process-service up"

    # orchestrator runs registerPolicyEnforcerConfiguration (etcd baseline
    # seed: agreements/datasets/archetypes/requestTypes/microservices)
    # synchronously, before its own HTTP server starts listening - so
    # waiting for its rollout to report Ready is a reliable proxy for "the
    # seed has already run" (successfully or not), through however many
    # restarts that takes. Live-found real flakiness here, unrelated to
    # etcd/DSP: orchestrator's own sidecar (RabbitMQ consumer proxy) isn't
    # always listening on 127.0.0.1:50051 yet on a genuinely fresh
    # deployment, causing a first-attempt FATAL crash that K8s
    # transparently restarts - the second attempt succeeds, but only once
    # rollout status finishes waiting through that cycle, issue #94.
    kubectl --context kind-dynamos -n orchestrator rollout status deployment/orchestrator --timeout=180s >/dev/null || return 1

    local gateway_ip
    gateway_ip=$(kind_gateway_ip)
    if [[ -z "$gateway_ip" ]]; then
        log_fail "could not determine the \"kind\" docker network's gateway IP"
        return 1
    fi
    log_info "kind network gateway (host, reachable from either cluster): ${gateway_ip}"

    log_info "wiring VU's real MVD identity (Secrets, ConfigMap, STS/job-fallback/EDR config, hostAliases)..."
    # STS_CLIENT_SECRET is sourced via secretKeyRef (ensure_env_from_ref
    # below) - env vars sourced from a Secret are read once at container
    # start, not live-updated when the Secret's own content later changes.
    # Applying a new secret value here is a silent no-op for any pod that's
    # already running unless it's explicitly restarted - live-found: this
    # is exactly what left negotiation-service-vu minting STS tokens with a
    # stale pre-wipe secret after mint_vu_participant rotated a real new
    # one, issue #94.
    local secret_changed=false old_secret
    for ns in negotiation-service transfer-process-service; do
        old_secret=$(kubectl --context kind-dynamos -n "$ns" get secret vu-sts-credentials -o jsonpath='{.data.client-secret}' 2>/dev/null | base64 -d 2>/dev/null || true)
        [[ "$old_secret" != "$VU_STS_CLIENT_SECRET" ]] && secret_changed=true
        kubectl --context kind-dynamos create secret generic vu-sts-credentials -n "$ns" \
            --from-literal=client-secret="${VU_STS_CLIENT_SECRET}" \
            --dry-run=client -o yaml | kubectl --context kind-dynamos apply -f - >/dev/null
    done
    kubectl --context kind-dynamos create configmap vu-default-job-request -n transfer-process-service \
        --from-literal=query.json="${DEFAULT_JOB_REQUEST_JSON}" \
        --dry-run=client -o yaml | kubectl --context kind-dynamos apply -f - >/dev/null

    ensure_env_var kind-dynamos negotiation-service negotiation-service-vu STS_TOKEN_URL "http://identityhub.provider.svc.cluster.local:7084/api/sts/token"
    ensure_env_var kind-dynamos negotiation-service negotiation-service-vu STS_CLIENT_ID "$VU_DID"
    ensure_env_from_ref kind-dynamos negotiation-service negotiation-service-vu STS_CLIENT_SECRET \
        '{"secretKeyRef":{"name":"vu-sts-credentials","key":"client-secret"}}'

    ensure_env_var kind-dynamos transfer-process-service transfer-process-service-vu STS_TOKEN_URL "http://identityhub.provider.svc.cluster.local:7084/api/sts/token"
    ensure_env_var kind-dynamos transfer-process-service transfer-process-service-vu STS_CLIENT_ID "$VU_DID"
    ensure_env_from_ref kind-dynamos transfer-process-service transfer-process-service-vu STS_CLIENT_SECRET \
        '{"secretKeyRef":{"name":"vu-sts-credentials","key":"client-secret"}}'
    ensure_env_var kind-dynamos transfer-process-service transfer-process-service-vu DEFAULT_JOB_TYPE "sqlDataRequest"
    ensure_env_from_ref kind-dynamos transfer-process-service transfer-process-service-vu DEFAULT_JOB_REQUEST \
        '{"configMapKeyRef":{"name":"vu-default-job-request","key":"query.json"}}'
    ensure_env_var kind-dynamos transfer-process-service transfer-process-service-vu CONNECTOR_BASE_URL "${VU_DSP_ADDRESS}"

    if [[ "$secret_changed" == true ]]; then
        log_info "VU's STS client secret changed - restarting negotiation-service-vu/transfer-process-service-vu so the env-sourced secret refreshes..."
        kubectl --context kind-dynamos -n negotiation-service rollout restart deployment/negotiation-service-vu >/dev/null
        kubectl --context kind-dynamos -n transfer-process-service rollout restart deployment/transfer-process-service-vu >/dev/null
    fi

    ensure_host_alias kind-dynamos dsp-connector dsp-connector-vu \
        "[{\"ip\":\"${gateway_ip}\",\"hostnames\":[\"identityhub.consumer.svc.cluster.local\"]}]"

    # VU_DSP_ADDRESS (top of this file) points at a NodePort Service that
    # isn't in any Helm chart - dsp-connector-vu's own Service is a plain
    # ClusterIP (see charts/dsp-connector), unreachable cross-cluster.
    # MVD's controlplane needs the real NodePort form to reach it directly
    # over the shared docker "kind" network (see the dynamos_node_ip
    # hostAlias below). This exact NodePort Service previously only ever
    # existed as a manual, undocumented one-off on the old kind-dynamos
    # cluster - lost silently on a genuine rebuild, issue #94. Declarative
    # + idempotent so a rerun is a no-op once it exists.
    cat <<EOF | kubectl --context kind-dynamos apply -f - >/dev/null
apiVersion: v1
kind: Service
metadata:
  name: dsp-vu
  namespace: dsp-connector
spec:
  type: NodePort
  selector:
    app: dsp-connector-vu
  ports:
    - port: 8080
      targetPort: 8080
      nodePort: 31464
EOF

    # VU's own catalog access model (pkg/api's Relations map, etcd key
    # /policyEnforcer/agreements/VU) has to actually know about MVD's
    # consumer DID, or catalogRequestHandler's own fetchCatalog rejects it
    # with "not-provisioned" - this Relation only ever existed as a manual
    # etcd write from an earlier debugging session, never captured anywhere
    # (etcd's own local-path-storage volume is node-local, so a genuine
    # `kind delete cluster` for kind-dynamos wipes it same as everything
    # else it holds), issue #94. Read-modify-write, idempotent - only adds
    # the key if it's genuinely missing, preserves every other existing
    # Relation (jorrit's own, in particular) untouched.
    local agreement
    for _ in $(seq 1 10); do
        agreement=$(kubectl --context kind-dynamos -n core exec etcd-0 -c etcd -- etcdctl get /policyEnforcer/agreements/VU --print-value-only 2>/dev/null)
        [[ -n "$agreement" ]] && break
        sleep 3
    done
    if [[ -z "$agreement" ]]; then
        log_fail "could not read /policyEnforcer/agreements/VU from etcd (orchestrator's own baseline seed never completed)"
        return 1
    fi
    if echo "$agreement" | jq -e --arg did "$MVD_CONSUMER_DID" '.relations[$did]' >/dev/null 2>&1; then
        log_ok "MVD consumer already has a Relation in VU's etcd agreement"
    else
        echo "$agreement" | jq --arg did "$MVD_CONSUMER_DID" \
            '.relations[$did] = {"ID":"mvd-consumer","requestTypes":["sqlDataRequest","genericRequest"],"dataSets":["wageGap"],"allowedArchetypes":["computeToData","dataThroughTtp"],"allowedComputeProviders":["SURF"]}' \
            | kubectl --context kind-dynamos -n core exec -i etcd-0 -c etcd -- etcdctl put /policyEnforcer/agreements/VU >/dev/null
        log_ok "MVD consumer Relation added to VU's etcd agreement"
    fi

    # dsp-connector's own DAT verification (dat_verification.go) defaults
    # to https - spec-correct, but nothing in this demo's identity layer
    # serves real TLS. A fresh dynamos-configuration.sh redeploy resets
    # this chart value back to that default even when an earlier manual
    # override is what actually made everything work - live-found: this
    # exact gap silently broke DAT verification for every inbound DSP
    # route on dsp-connector-vu (catalog, negotiation, transfer) after a
    # setup run, not just the catalog step where it happened to surface
    # first, issue #94.
    ensure_env_var kind-dynamos dsp-connector dsp-connector-vu DID_WEB_SCHEME "http"
    ensure_host_alias kind-dynamos negotiation-service negotiation-service-vu \
        "[{\"ip\":\"${gateway_ip}\",\"hostnames\":[\"controlplane.consumer.svc.cluster.local\",\"identityhub.provider.svc.cluster.local\"]}]"
    ensure_host_alias kind-dynamos transfer-process-service transfer-process-service-vu \
        "[{\"ip\":\"${gateway_ip}\",\"hostnames\":[\"controlplane.consumer.svc.cluster.local\",\"identityhub.provider.svc.cluster.local\"]}]"

    kubectl --context kind-dynamos -n negotiation-service rollout status deployment/negotiation-service-vu --timeout=90s >/dev/null || return 1
    kubectl --context kind-dynamos -n transfer-process-service rollout status deployment/transfer-process-service-vu --timeout=90s >/dev/null || return 1
    kubectl --context kind-dynamos -n dsp-connector rollout status deployment/dsp-connector-vu --timeout=90s >/dev/null || return 1
    log_ok "VU wired for real DCP-compliant external interop"

    # Reciprocal direction: MVD's own consumer controlplane dispatches every
    # outbound DSP call (catalog/negotiation/transfer) to VU_DSP_ADDRESS's
    # cluster-internal DNS name directly - unlike the bridged calls above,
    # this hits DYNAMOS's real NodePort service (31464), which is reachable
    # cross-cluster via the shared docker "kind" network using the target
    # node's own container IP (not the gateway_ip - that's only for
    # reaching a host-bound port-forward, same distinction the earlier
    # gateway_ip/mvd_ip bug in this file was about). Live-found this
    # specific hostAlias only ever existed as a manual live patch on a
    # now-deleted kind-mvd cluster, never captured anywhere - a genuine
    # kind-mvd rebuild loses it silently, issue #94.
    local dynamos_node_ip
    dynamos_node_ip=$(kind_node_ip dynamos-control-plane)
    if [[ -z "$dynamos_node_ip" ]]; then
        log_fail "could not determine kind-dynamos node's own container IP"
        return 1
    fi
    ensure_host_alias kind-mvd consumer controlplane \
        "[{\"ip\":\"${dynamos_node_ip}\",\"hostnames\":[\"dsp-vu.dsp-connector.svc.cluster.local\"]}]"
    kubectl --context kind-mvd -n consumer rollout status deployment/controlplane --timeout=90s >/dev/null || return 1
    log_ok "MVD's consumer controlplane wired to reach VU directly"
}

# setup_mvd_cluster brings up kind-mvd itself from nothing: the node, the
# Traefik/Gateway-API layer, MVD's own app images (built from source), and
# the app manifests - everything wiki/devops/mvd-demo-dataspace-setup.md
# documents as a manual runbook, made idempotent and self-healing. Every
# step below checks real state first and only acts when something is
# genuinely missing, same philosophy as setup_mvd (identity layer, called
# right after this) and setup_dynamos. Real cost when the node itself is
# missing: minutes, dominated by the gradlew dockerize build - unavoidable,
# this is what "everything both demos need, from a cold machine boot"
# actually requires for MVD's side, issue #94.
setup_mvd_cluster() {
    log_step "Setup: MVD cluster + app deployment (kind-mvd)"

    if ! kubectl config get-contexts kind-mvd &>/dev/null; then
        log_info "kind-mvd cluster missing - creating..."
        if [[ ! -f "${MVD_ROOT}/kind-config.yaml" ]]; then
            cat > "${MVD_ROOT}/kind-config.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
EOF
        fi
        ( cd "${MVD_ROOT}" && kind create cluster -n mvd --config kind-config.yaml ) \
            || { log_fail "kind create cluster -n mvd failed"; return 1; }
        log_ok "kind-mvd node created"
    else
        # A kubectl context existing only means kind-mvd was created at
        # some point - the node container itself can be stopped (e.g. a
        # deliberate `docker stop mvd-control-plane` teardown, a host
        # reboot) without the context ever being removed. Every call below
        # assumes a genuinely reachable API server - live-found running
        # this for real after a docker-stop-only teardown, issue #94.
        if ! docker ps --filter name=mvd-control-plane --filter status=running -q | grep -q .; then
            log_info "mvd-control-plane exists but isn't running - starting it..."
            docker start mvd-control-plane >/dev/null
            sleep 10
            # The API server's own host port (docker's own NAT mapping for
            # container port 6443) is not guaranteed to survive a stop -
            # kubeconfig can end up pointing at a now-stale port, "dial tcp
            # 127.0.0.1:<old-port>: connect: connection refused" even
            # though the container itself is genuinely Running. Re-export
            # unconditionally after a real start - a no-op write if the
            # port didn't actually change. Live-found running this for
            # real after a docker-stop-then-start cycle, issue #94.
            kind export kubeconfig --name mvd >/dev/null 2>&1
        fi
        for _ in $(seq 1 30); do
            kubectl --context kind-mvd get ns >/dev/null 2>&1 && break
            sleep 2
        done
        log_ok "kind-mvd node already exists"
    fi
    ensure_coredns kind-mvd mvd-control-plane || return 1

    if ! kubectl --context kind-mvd -n traefik get deployment traefik &>/dev/null; then
        log_info "Traefik missing - installing..."
        # A helm release can say "deployed" while the actual Deployment is
        # gone - `kubectl delete deployments --all` (a real teardown this
        # session used) removes the resource without telling Helm anything
        # changed, so a plain `helm upgrade -i` with identical values sees
        # no diff and silently no-ops instead of recreating it. Uninstall
        # any such stale release first so the install below is a genuine
        # fresh one. Live-found running this for real, issue #94.
        if helm status traefik -n traefik --kube-context kind-mvd >/dev/null 2>&1; then
            log_info "traefik helm release exists but its Deployment doesn't - uninstalling first..."
            helm uninstall traefik -n traefik --kube-context kind-mvd --wait >/dev/null 2>&1
            # --wait above blocks until helm's own uninstall hooks finish,
            # but the release record itself can take a moment longer to
            # actually disappear from `helm status` - a real race
            # otherwise: the immediate reinstall below can start while the
            # old release is still mid-teardown, live-found running this
            # for real, issue #94.
            for _ in $(seq 1 15); do
                helm status traefik -n traefik --kube-context kind-mvd >/dev/null 2>&1 || break
                sleep 1
            done
        fi
        # Chart 41.x's own schema stopped accepting values.yaml's "logs" key
        # (real upstream drift since this repo's values.yaml was last
        # touched, unrelated to anything DYNAMOS-side) - 40.3.0 is the
        # newest version confirmed to still accept it unmodified.
        helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1
        helm repo update >/dev/null 2>&1
        helm upgrade -i --namespace traefik traefik traefik/traefik --create-namespace \
            -f "${MVD_ROOT}/values.yaml" --version 40.3.0 >/dev/null \
            || { log_fail "traefik install failed"; return 1; }
        kubectl --context kind-mvd rollout status deployment/traefik -n traefik --timeout=120s >/dev/null \
            || { log_fail "traefik did not become ready"; return 1; }
        log_ok "Traefik installed"
    else
        log_ok "Traefik already installed"
    fi

    if ! kubectl --context kind-mvd get crd gateways.gateway.networking.k8s.io &>/dev/null; then
        log_info "Gateway API CRDs missing - applying..."
        kubectl --context kind-mvd apply --server-side --force-conflicts \
            -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml >/dev/null \
            || { log_fail "gateway API CRD apply failed"; return 1; }
        log_ok "Gateway API CRDs applied"
    else
        log_ok "Gateway API CRDs already present"
    fi

    local img need_build=false
    for img in controlplane dataplane identity-hub issuerservice; do
        docker image inspect "ghcr.io/eclipse-edc/minimumviabledataspace/${img}:latest" >/dev/null 2>&1 || need_build=true
    done
    if $need_build; then
        log_info "MVD images missing - building from source (0.17.0, this takes a while)..."
        ( cd "${MVD_ROOT}" && ./gradlew dockerize ) > "${SCRATCH_DIR}/gradlew-dockerize.log" 2>&1 \
            || { log_fail "gradlew dockerize failed, see ${SCRATCH_DIR}/gradlew-dockerize.log"; return 1; }
        log_ok "MVD images built from source"
    else
        log_ok "MVD images already built"
    fi

    for img in controlplane dataplane identity-hub issuerservice; do
        if ! docker exec mvd-control-plane crictl images 2>/dev/null | grep -q "minimumviabledataspace/${img} "; then
            log_info "loading ${img} into kind-mvd node..."
            kind load docker-image "ghcr.io/eclipse-edc/minimumviabledataspace/${img}:latest" --name mvd \
                || { log_fail "kind load docker-image failed for ${img}"; return 1; }
        fi
    done
    log_ok "MVD images present on the node"

    # Always apply, unconditionally - unlike the helm-managed installs
    # above, `kubectl apply -k` has no "looks unchanged, skip it" pitfall:
    # it reconciles from the manifests every time, recreating anything
    # actually missing and no-op'ing on anything already correct (Jobs
    # included - a COMPLETED Job's own spec is immutable, so reapplying
    # never re-triggers it). The old "only apply if namespace 'consumer'
    # is missing" gate used namespace existence as a proxy for "are the
    # Deployments here" - wrong signal: `kubectl delete deployments --all`
    # (a real teardown mode this session used) leaves every namespace
    # intact while wiping every Deployment inside them, so that gate never
    # fired and the missing Deployments were never recreated. Live-found
    # running this for real, issue #94.
    kubectl --context kind-mvd apply -k "${MVD_ROOT}/k8s" >/dev/null \
        || { log_fail "kubectl apply -k k8s failed"; return 1; }

    local ns dep
    # vault-bootstrap's own Job-Completed status is never trustworthy on
    # its own: it writes real state (the "participants" secrets engine
    # mount, secret/data/aes-key-alias) into Vault's own inmem storage -
    # wiped on every Vault pod restart, but the Job object that created it
    # stays "Completed" forever and never re-runs by itself. This has to
    # run BEFORE the Deployment-rollout wait below, not after: issuerservice
    # and every other app in these namespaces reads its own datasource
    # config from Vault at startup, so a wiped Vault blocks the apps from
    # ever becoming Ready, not just the seed jobs. Live-found running this
    # for real, issue #94.
    for ns in issuer consumer provider; do
        if kubectl --context kind-mvd exec -n "$ns" deploy/vault -- sh -c \
            "VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root vault kv get -field=content secret/aes-key-alias" >/dev/null 2>&1; then
            continue
        fi
        log_info "${ns}:vault-bootstrap's own Job says Completed but its real Vault secret is gone (Vault's inmem storage got wiped) - re-running..."
        kubectl --context kind-mvd delete job vault-bootstrap -n "$ns" --ignore-not-found >/dev/null
        kubectl --context kind-mvd apply -k "${MVD_ROOT}/k8s" >/dev/null
        kubectl --context kind-mvd -n "$ns" wait --for=condition=complete job/vault-bootstrap --timeout=120s >/dev/null \
            || { log_fail "${ns}:vault-bootstrap did not complete, even after a retry"; return 1; }
        # The apps in this namespace may already be up and crash-looping
        # from having started (and failed to read their own datasource
        # secret) before vault-bootstrap just now actually finished - EDC's
        # own Vault client backs off rather than polling indefinitely, so
        # simply becoming available later doesn't get picked up on its own.
        # A rollout restart forces a clean read against the now-real
        # secret. Live-found running this for real, issue #94.
        local app
        for app in issuerservice controlplane identityhub dataplane; do
            kubectl --context kind-mvd -n "$ns" get deployment "$app" >/dev/null 2>&1 \
                && kubectl --context kind-mvd -n "$ns" rollout restart deployment/"$app" >/dev/null 2>&1
        done
    done

    local ns_dep
    for ns_dep in issuer:issuerservice issuer:postgres issuer:vault \
                  consumer:controlplane consumer:identityhub consumer:postgres consumer:vault \
                  provider:controlplane provider:dataplane provider:identityhub provider:postgres provider:vault \
                  mvd-common:keycloak mvd-common:postgres; do
        kubectl --context kind-mvd -n "${ns_dep%%:*}" rollout status deployment/"${ns_dep##*:}" --timeout=180s >/dev/null \
            || { log_fail "${ns_dep} did not become ready"; return 1; }
    done

    # Same reasoning as the apply above: always wait, unconditionally.
    # `kubectl wait --for=condition=complete` on an already-Completed Job
    # returns immediately, so this costs nothing when the seed data is
    # already real - and catches the genuine "Jobs got deleted too, need a
    # fresh run" case the old just_deployed-gated version missed.
    # vault-bootstrap itself is handled above, before the Deployment wait -
    # not repeated here.
    for ns_dep in issuer:issuerservice-seed consumer:controlplane-seed consumer:identityhub-seed \
                  provider:controlplane-seed provider:identityhub-seed; do
        ns="${ns_dep%%:*}"; dep="${ns_dep##*:}"
        if kubectl --context kind-mvd -n "$ns" wait --for=condition=complete job/"$dep" --timeout=180s >/dev/null 2>&1; then
            continue
        fi
        # Keycloak's own admin-bootstrap login has a real internal
        # startup race: the job's own init container only waits for
        # Keycloak's HTTP server to answer, not for its admin grant to
        # actually be usable yet - a first attempt can create the
        # "issuer" participant successfully in Step 1/2 and then fail a
        # later step, leaving a stale row that makes every retry hit a
        # 409 Conflict the job's own script doesn't tolerate (crash
        # loop, not a clean failure). Live-found running this for real,
        # issue #94 - same class of "stale half-finished seed" this
        # script's own setup_mvd already documents for VU's participant.
        if [[ "$dep" == "issuerservice-seed" ]]; then
            log_info "${ns_dep} failed - clearing a possible stale 'issuer' participant row before retrying..."
            kubectl --context kind-mvd exec -n issuer deploy/postgres -- psql -U issuer -d issuerservice \
                -tAc "DELETE FROM participant_context WHERE participant_context_id='issuer';" >/dev/null 2>&1
        fi
        log_info "${ns_dep} did not complete - deleting and retrying once..."
        kubectl --context kind-mvd delete job "$dep" -n "$ns" --ignore-not-found >/dev/null
        kubectl --context kind-mvd apply -k "${MVD_ROOT}/k8s" >/dev/null
        kubectl --context kind-mvd -n "$ns" wait --for=condition=complete job/"$dep" --timeout=180s >/dev/null \
            || { log_fail "${ns_dep} (manifest-created seed job) did not complete, even after a retry"; return 1; }
    done
    log_ok "MVD cluster + app deployment ready"
}

# setup_mvd re-seeds MVD's own identity data (vault-bootstrap,
# issuerservice-seed, identityhub-seed, VU's own participant) if it's
# genuinely absent - setup_mvd_cluster (above, called first) is what
# guarantees the cluster and app pods this depends on actually exist.
setup_mvd() {
    log_step "Setup: MVD identity layer (kind-mvd)"

    if ! docker ps --filter name=mvd-control-plane --filter status=running -q | grep -q .; then
        log_info "mvd-control-plane not running, starting it..."
        docker start mvd-control-plane >/dev/null
        sleep 15
    fi
    for _ in $(seq 1 30); do
        kubectl --context kind-mvd get ns >/dev/null 2>&1 && break
        sleep 2
    done
    log_ok "kind-mvd node running"

    # A count query's own psql error (e.g. "relation does not exist" -
    # live-found after wiping Postgres without also restarting the app pod
    # that migrates its schema, issue #94) goes to /dev/null, leaving stdout
    # empty rather than "0". Every caller below tests `!= "0"` to decide
    # "already seeded" - an empty string also satisfies that, so a broken
    # query used to read as "seeded" instead of "needs reseeding". Normalize
    # empty to "0" here, once, so every caller's own check stays correct
    # regardless of which failure mode produced it.
    mvd_psql() {
        local out
        out=$(kubectl --context kind-mvd exec -n "$1" deploy/postgres -- psql -U "$2" -d "$3" -tAc "$4" 2>/dev/null | tr -d '[:space:]')
        echo "${out:-0}"
    }

    local issuer_count
    issuer_count=$(mvd_psql issuer issuer issuerservice "SELECT count(*) FROM participant_context;" | tr -d '[:space:]')
    if [[ "$issuer_count" != "0" ]]; then
        log_ok "issuer tenant already seeded (${issuer_count} participant context(s))"
    else
        log_info "issuer tenant missing - re-seeding (issuerservice-seed)..."
        # vault-bootstrap is NOT redone here: setup_mvd_cluster (called
        # before this function) already checks each namespace's real Vault
        # secret (secret/aes-key-alias) and re-runs vault-bootstrap itself
        # if it's genuinely gone. Blindly re-deleting+reapplying it again
        # here, unconditionally, collided with that already-correct state
        # ("path already in use" duplicate-mount crash loop) - live-found,
        # issue #94.
        # apply -k, not a raw apply -f: the raw file's own spec can diverge
        # from kustomize's rendered one, and a Job's spec.template is
        # immutable once created - the next unconditional `apply -k` in
        # setup_mvd_cluster then fails outright ("field is immutable")
        # trying to reconcile a live Job it didn't create. Live-found,
        # issue #94.
        kubectl --context kind-mvd delete job issuerservice-seed -n issuer --ignore-not-found >/dev/null
        kubectl --context kind-mvd apply -k "${MVD_ROOT}/k8s" >/dev/null
        kubectl --context kind-mvd wait --for=condition=complete job/issuerservice-seed -n issuer --timeout=120s >/dev/null || { log_fail "issuerservice-seed failed - check for a stale half-created 'issuer' participant (DELETE FROM participant_context WHERE participant_context_id='issuer' in issuerservice's own DB, then retry)"; return 1; }
        log_ok "issuer tenant seeded"
    fi

    local consumer_count
    consumer_count=$(mvd_psql consumer cp identityhub "SELECT count(*) FROM participant_context;" | tr -d '[:space:]')
    if [[ "$consumer_count" != "0" ]]; then
        log_ok "MVD consumer identity already seeded"
    else
        log_info "MVD consumer identity missing - re-seeding (identityhub-seed)..."
        # apply -k, not a raw apply -f - same immutable-spec conflict as
        # issuerservice-seed above.
        kubectl --context kind-mvd delete job identityhub-seed -n consumer --ignore-not-found >/dev/null
        kubectl --context kind-mvd apply -k "${MVD_ROOT}/k8s" >/dev/null
        kubectl --context kind-mvd wait --for=condition=complete job/identityhub-seed -n consumer --timeout=180s >/dev/null || { log_fail "identityhub-seed (consumer) failed"; return 1; }
        log_ok "MVD consumer identity seeded"
    fi

    local vu_count vu_cred_count
    vu_count=$(mvd_psql provider cp identityhub "SELECT count(*) FROM participant_context WHERE identity='${VU_DID}';" | tr -d '[:space:]')
    vu_cred_count=$(mvd_psql provider cp identityhub "SELECT count(*) FROM credential_resource WHERE participant_context_id='vu';" | tr -d '[:space:]')
    if [[ "$vu_count" != "0" && "$vu_cred_count" != "0" ]]; then
        # VU_STS_CLIENT_SECRET's own module-level default is only a
        # fallback for a first-ever run - an already-existing participant
        # means some earlier run (this one or a prior one) already minted a
        # real secret, and Vault (not this script's own memory) is the
        # only durable record of what it actually is. Re-reading it here
        # unconditionally is what makes a plain rerun safe: skip this and a
        # rerun would silently overwrite DYNAMOS's real k8s Secret with the
        # stale hardcoded default below, issue #94.
        local vault_secret
        vault_secret=$(kubectl --context kind-mvd exec -n provider deploy/vault -- sh -c \
            "VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root vault kv get -field=content secret/vu-sts-client-secret" 2>/dev/null)
        if [[ -z "$vault_secret" ]]; then
            log_fail "VU's participant exists but its real STS secret could not be read back from Vault (alias vu-sts-client-secret)"
            return 1
        fi
        VU_STS_CLIENT_SECRET="$vault_secret"
        log_ok "VU's real MVD participant already exists (real STS secret re-read from Vault)"
    else
        if [[ "$vu_count" != "0" ]]; then
            # A participant context with no issued credentials is a
            # half-finished mint (e.g. an earlier Step-2-only version of
            # mint_vu_participant, or an interrupted run) - MVD's own
            # consumer controlplane rejects VU's outbound messages outright
            # in this state ("Number of requested credentials does not
            # match the number of returned credentials", live-found, issue
            # #94). Real Delete Participant call (not a raw DB DELETE - the
            # participant's keypair/DID rows have real FK dependents this
            # respects correctly), then a clean re-mint below.
            log_info "VU's participant context exists but has no issued credentials - deleting and re-minting cleanly..."
            delete_vu_participant || return 1
        fi
        log_info "VU's real MVD participant missing - minting fresh..."
        mint_vu_participant || return 1
    fi
}

# delete_vu_participant removes an existing (incomplete) "vu" participant
# context from provider's IdentityHub via the real Participant Context
# Mgmt API's own DELETE, using the same Keycloak "provisioner" client
# mint_vu_participant's own Step 2 uses. Confirmed live: DELETE
# .../api/identity/v1alpha/participants/vu (raw participantContextId, not
# base64) returns 204 and cleanly removes the row plus its keypair/DID
# dependents.
delete_vu_participant() {
    local pod_script
    pod_script='
set -e
TOKEN=$(curl -sf -X POST "http://keycloak.mvd-common.svc.cluster.local:8080/realms/mvd/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" -d "client_id=provisioner" -d "client_secret=provisioner-secret" \
    -d "scope=issuer-admin-api:write" | sed -n "s/.*\"access_token\":\"\([^\"]*\)\".*/\1/p")
if [ -z "$TOKEN" ]; then echo "ERROR: no provisioner token" >&2; exit 1; fi
STATUS=$(curl -sS -o /dev/null -w "%{http_code}" -X DELETE "http://identityhub.provider.svc.cluster.local:7081/api/identity/v1alpha/participants/vu" \
    -H "Authorization: Bearer $TOKEN")
if [ "$STATUS" != "204" ] && [ "$STATUS" != "404" ]; then
    echo "ERROR: delete failed, status=$STATUS" >&2; exit 1
fi
'
    kubectl --context kind-mvd delete pod delete-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
    kubectl --context kind-mvd run delete-vu-participant -n provider --restart=Never --quiet \
        --image=curlimages/curl:latest \
        -- sh -c "$pod_script" >/dev/null 2>&1

    if ! kubectl --context kind-mvd wait --for=jsonpath='{.status.phase}'=Succeeded pod/delete-vu-participant -n provider --timeout=60s >/dev/null 2>&1; then
        kubectl --context kind-mvd logs delete-vu-participant -n provider > "${SCRATCH_DIR}/delete-vu-participant.log" 2>&1
        kubectl --context kind-mvd delete pod delete-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
        log_fail "delete-vu-participant pod did not complete, see ${SCRATCH_DIR}/delete-vu-participant.log"
        return 1
    fi
    kubectl --context kind-mvd delete pod delete-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
    log_ok "stale VU participant context deleted"
}

# mint_vu_participant creates VU's own real participant context on
# provider's IdentityHub - all 4 steps dynamos-mvd's own
# k8s/provider/application/identityhub-seed.yaml uses to create its own
# "provider-participant" (same Keycloak OAuth clients: issuer/issuer-secret
# for Step 1, provisioner/provisioner-secret for Step 2, admin/
# edc-v-admin-secret for Steps 3-4), just parameterized for participant
# "vu" / VU_DID instead. A minimal Step-2-only mint (just DID+key, no
# credentials) was tried first and looked sufficient - DYNAMOS's own
# dat_verification.go never checks embedded credentials - but MVD's own
# consumer controlplane does, for outbound messages VU sends TO it: its
# rejection was "Unauthorized: Number of requested credentials does not
# match the number of returned credentials", live-found, issue #94. A real,
# unmodified DSP peer enforces the full DCP presentation flow even when
# DYNAMOS's own inbound side doesn't - so VU needs the same
# MembershipCredential + ManufacturerCredential pair every other MVD
# participant gets.
#
# Runs the real curl recipe inside a throwaway pod in kind-mvd's own
# provider namespace (needs cluster-internal DNS for keycloak.mvd-common,
# identityhub.provider and issuerservice.issuer - not reachable from the
# host/devcontainer directly). Only Step 2's own response JSON is ever
# echoed to stdout (steps 1/3/4 report failures to stderr instead) so the
# host side can parse it cleanly. On success, overwrites the calling
# shell's own VU_STS_CLIENT_SECRET with the real secret IdentityHub just
# generated for this participant's own STS account.
mint_vu_participant() {
    local pod_script
    pod_script='
set -e

# Step 1: create Holder in IssuerService
ISSUER_TOKEN=$(curl -sf -X POST "http://keycloak.mvd-common.svc.cluster.local:8080/realms/mvd/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" -d "client_id=issuer" -d "client_secret=issuer-secret" \
    -d "scope=issuer-admin-api:write" | sed -n "s/.*\"access_token\":\"\([^\"]*\)\".*/\1/p")
if [ -z "$ISSUER_TOKEN" ]; then echo "ERROR: no issuer token" >&2; exit 1; fi
HOLDER_STATUS=$(curl -sS -o /dev/null -w "%{http_code}" -X POST "http://issuerservice.issuer.svc.cluster.local:10013/api/admin/v1alpha/participants/issuer/holders" \
    -H "Authorization: Bearer $ISSUER_TOKEN" -H "Content-Type: application/json" \
    -d "{\"did\":\"$DID\",\"holderId\":\"$DID\",\"name\":\"VU Participant\"}")
if [ "$HOLDER_STATUS" != "200" ] && [ "$HOLDER_STATUS" != "201" ] && [ "$HOLDER_STATUS" != "409" ]; then
    echo "ERROR: holder creation failed, status=$HOLDER_STATUS" >&2; exit 1
fi

# Step 2: create Participant Context in IdentityHub
PROVISIONER_TOKEN=$(curl -sf -X POST "http://keycloak.mvd-common.svc.cluster.local:8080/realms/mvd/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" -d "client_id=provisioner" -d "client_secret=provisioner-secret" \
    -d "scope=issuer-admin-api:write" | sed -n "s/.*\"access_token\":\"\([^\"]*\)\".*/\1/p")
if [ -z "$PROVISIONER_TOKEN" ]; then echo "ERROR: no provisioner token" >&2; exit 1; fi
PARTICIPANT_RESP=$(curl -sS -X POST "http://identityhub.provider.svc.cluster.local:7081/api/identity/v1alpha/participants/" \
    -H "Authorization: Bearer $PROVISIONER_TOKEN" -H "Content-Type: application/json" \
    -d "{\"roles\":[],\"serviceEndpoints\":[{\"type\":\"CredentialService\",\"serviceEndpoint\":\"http://identityhub.provider.svc.cluster.local:7082/api/credentials/v1/participants/vu\",\"id\":\"vu-credentialservice-1\"},{\"type\":\"ProtocolEndpoint\",\"serviceEndpoint\":\"$DSP_ADDR\",\"id\":\"vu-dsp\"}],\"active\":true,\"participantId\":\"$DID\",\"participantContextId\":\"vu\",\"did\":\"$DID\",\"key\":{\"keyId\":\"$DID#key-1\",\"privateKeyAlias\":\"$DID#key-1\",\"keyGeneratorParams\":{\"algorithm\":\"EC\"}}}")

# Step 3: request credential issuance (Membership + Manufacturer, same pair provider-participant gets)
ADMIN_TOKEN=$(curl -sf -X POST "http://keycloak.mvd-common.svc.cluster.local:8080/realms/mvd/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" -d "client_id=admin" -d "client_secret=edc-v-admin-secret" \
    -d "scope=identity-api:write" | sed -n "s/.*\"access_token\":\"\([^\"]*\)\".*/\1/p")
if [ -z "$ADMIN_TOKEN" ]; then echo "ERROR: no admin token" >&2; exit 1; fi
HOLDER_PID="vu-credential-request-1"
CRED_STATUS=$(curl -sS -o /dev/null -w "%{http_code}" -X POST "http://identityhub.provider.svc.cluster.local:7081/api/identity/v1alpha/participants/vu/credentials/request" \
    -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d "{\"issuerDid\":\"did:web:issuerservice.issuer.svc.cluster.local%3A10016:issuer\",\"holderPid\":\"$HOLDER_PID\",\"credentials\":[{\"format\":\"VC1_0_JWT\",\"type\":\"MembershipCredential\",\"id\":\"membership-credential-def\"},{\"format\":\"VC1_0_JWT\",\"type\":\"ManufacturerCredential\",\"id\":\"manufacturer-credential-def\"}]}")
if [ "$CRED_STATUS" != "201" ]; then
    echo "ERROR: credential request failed, status=$CRED_STATUS" >&2; exit 1
fi

# Step 4: wait for ISSUED
STATUS=""
i=0
while [ "$STATUS" != "ISSUED" ]; do
    i=$((i + 1))
    if [ "$i" -gt 30 ]; then echo "ERROR: credentials not ISSUED after 30 attempts" >&2; exit 1; fi
    sleep 2
    BODY=$(curl -sS "http://identityhub.provider.svc.cluster.local:7081/api/identity/v1alpha/participants/vu/credentials/request/$HOLDER_PID" \
        -H "Authorization: Bearer $ADMIN_TOKEN")
    STATUS=$(echo "$BODY" | sed -n "s/.*\"status\":\"\([^\"]*\)\".*/\1/p")
done

echo "$PARTICIPANT_RESP"
'
    kubectl --context kind-mvd delete pod mint-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
    kubectl --context kind-mvd run mint-vu-participant -n provider --restart=Never --quiet \
        --image=curlimages/curl:latest \
        --env="DID=${VU_DID}" --env="DSP_ADDR=${VU_DSP_ADDRESS}" \
        -- sh -c "$pod_script" >/dev/null 2>&1

    if ! kubectl --context kind-mvd wait --for=jsonpath='{.status.phase}'=Succeeded pod/mint-vu-participant -n provider --timeout=120s >/dev/null 2>&1; then
        kubectl --context kind-mvd logs mint-vu-participant -n provider > "${SCRATCH_DIR}/mint-vu-participant.log" 2>&1
        kubectl --context kind-mvd delete pod mint-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
        log_fail "mint-vu-participant pod did not complete, see ${SCRATCH_DIR}/mint-vu-participant.log"
        return 1
    fi

    local resp secret
    resp=$(kubectl --context kind-mvd logs mint-vu-participant -n provider 2>/dev/null)
    kubectl --context kind-mvd delete pod mint-vu-participant -n provider --ignore-not-found >/dev/null 2>&1
    secret=$(echo "$resp" | jq -r '.clientSecret // empty' 2>/dev/null)
    if [[ -z "$secret" ]]; then
        log_fail "no clientSecret in mint response: ${resp}"
        return 1
    fi
    VU_STS_CLIENT_SECRET="$secret"
    log_ok "VU's real MVD participant minted (holder + participant context + MembershipCredential/ManufacturerCredential ISSUED), new STS client secret captured"
}

# ensure_fixture_did creates the fixture-did Deployment+Service dsp-
# transfer-demo.sh's own DYNAMOS-to-DYNAMOS identity (a self-hosted DID,
# unrelated to MVD) needs - nothing in this repo tracks it in any chart
# (grepped the whole repo - confirmed). mint-identity.sh (and the Go
# program it calls) both only *restart* this Deployment to publish a
# freshly-minted DID document into its ConfigMap - they assume it already
# exists. A minimal nginx pod serving the ConfigMap-mounted did.json at the
# exact path did:web resolution requests - no linkerd injection, nothing
# DSP-specific beyond serving one static file. Safe to call every time:
# kubectl apply is a no-op if it already matches.
ensure_fixture_did() {
    if kubectl --context kind-dynamos -n dsp-connector get deployment fixture-did >/dev/null 2>&1 \
        && kubectl --context kind-dynamos -n dsp-connector get svc fixture-did >/dev/null 2>&1; then
        log_ok "fixture-did already exists"
        return 0
    fi

    log_step "Creating the fixture-did Deployment+Service (missing, not tracked in any chart)"
    kubectl --context kind-dynamos -n dsp-connector get configmap fixture-did >/dev/null 2>&1 \
        || kubectl --context kind-dynamos -n dsp-connector create configmap fixture-did --from-literal=did.json='{}' >/dev/null

    kubectl --context kind-dynamos apply -f - >/dev/null <<EOF
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

    if kubectl --context kind-dynamos -n dsp-connector rollout status deployment/fixture-did --timeout=60s >/dev/null 2>&1; then
        log_ok "fixture-did created"
    else
        log_fail "fixture-did did not come up - is the cluster actually reachable? (see errors above)"
        return 1
    fi
}

# setup_internal_demo covers everything dsp-transfer-demo.sh's own
# DYNAMOS-to-DYNAMOS demo needs on top of what setup_dynamos already did:
# the fixture-did identity server, the two image/env patches no Helm chart
# wires up (agent's own dsp-latest tag, dsp-connector-vu's own
# CONNECTOR_BASE_URL, dsp-connector-uva's own DID_WEB_SCHEME).
setup_internal_demo() {
    log_step "Setup: DYNAMOS-to-DYNAMOS demo (dsp-transfer-demo.sh)"

    ensure_fixture_did || return 1

    # Every chart's own dynamos1/<service>:main image on Docker Hub is the
    # real, current build for every service except agent: its own job-
    # name-length fix (issue #97) isn't merged to main yet, so its chart's
    # default :main doesn't have it - dynamos1/agent:${AGENT_IMAGE_TAG}
    # does (built and pushed from this branch).
    kubectl --context kind-dynamos -n uva set image deployment/uva uva="dynamos1/agent:${AGENT_IMAGE_TAG}" >/dev/null
    kubectl --context kind-dynamos -n vu set image deployment/vu vu="dynamos1/agent:${AGENT_IMAGE_TAG}" >/dev/null

    # The sql-query compute-job image agent deploys per request is picked at
    # runtime via the SQL-QUERY_TAG env var (getMicroserviceTag,
    # go/cmd/agent/deploy_job.go) - falls back to :main if unset. The
    # employeeSurvey dataset's own CSV data only exists in
    # dynamos1/sql-query:${SQL_QUERY_TAG}, not :main.
    kubectl --context kind-dynamos -n uva set env deployment/uva "SQL-QUERY_TAG=${SQL_QUERY_TAG}" >/dev/null
    kubectl --context kind-dynamos -n vu set env deployment/vu "SQL-QUERY_TAG=${SQL_QUERY_TAG}" >/dev/null

    kubectl --context kind-dynamos -n uva rollout status deployment/uva --timeout=90s >/dev/null 2>&1
    kubectl --context kind-dynamos -n vu rollout status deployment/vu --timeout=90s >/dev/null 2>&1

    # dsp-connector-vu needs this to build its own outbound callbackAddress
    # - empty by default (charts/dsp-connector's own placeholder), which
    # broke every push UVA tried to send back.
    kubectl --context kind-dynamos -n dsp-connector set env deployment/dsp-connector-vu \
        CONNECTOR_BASE_URL="http://dsp-connector-vu.dsp-connector.svc.cluster.local:8080" >/dev/null
    # prod defaults to https, but fixture-did serves plain HTTP - DAT
    # verification silently fails without this. setup_dynamos already set
    # this on dsp-connector-vu; UVA's own side needs it too for this demo.
    ensure_env_var kind-dynamos dsp-connector dsp-connector-uva DID_WEB_SCHEME "http"

    kubectl --context kind-dynamos -n dsp-connector rollout status deployment/dsp-connector-vu --timeout=90s >/dev/null || return 1
    kubectl --context kind-dynamos -n dsp-connector rollout status deployment/dsp-connector-uva --timeout=90s >/dev/null || return 1
    log_ok "DYNAMOS-to-DYNAMOS demo wired"
}

main() {
    ensure_tools
    setup_mvd_cluster || exit 1
    setup_mvd || exit 1
    setup_dynamos || exit 1
    setup_internal_demo || exit 1
    log_step "Setup complete"
    log_ok "run dsp-external-consumer-demo.sh or dsp-transfer-demo.sh directly - no further setup needed"
}

main "$@"
