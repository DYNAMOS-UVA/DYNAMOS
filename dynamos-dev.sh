#!/usr/bin/env bash
# Local DYNAMOS dev environment (kind cluster + local registry + Tilt).
#
#   ./dynamos-dev.sh up      Make the environment ready (first time: 5-10 min).
#   tilt up                  Build and deploy the services (Ctrl+C to stop).
#   ./dynamos-dev.sh down    Stop all containers. The data stays.
#   ./dynamos-dev.sh reset   Delete the cluster and the registry.
#   ./dynamos-dev.sh status  Show the state of the containers.
#   ./dynamos-dev.sh tokens  Show the dev auth tokens as shell exports.
#
# Requirements on the host: docker, curl, tar, openssl.
#
# The auth keys and dev tokens are in .dev/auth (see scripts/auth-secrets.sh).
# The script downloads kind, kubectl, helm, tilt and linkerd (pinned
# versions) into .dev/bin. It does not change the tools on your PATH.
#
# See docs/development_guide/Develop/Tilt.md.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${REPO_ROOT}/.dev/bin"
USER_PATH="${PATH}"
export PATH="${BIN_DIR}:${PATH}"

CLUSTER="dynamos"
AUTH_KEYS_DIR="${REPO_ROOT}/.dev/auth"
CONTEXT="kind-${CLUSTER}"
NODE="${CLUSTER}-control-plane"
NODE_IMAGE="kindest/node:v1.30.0@sha256:047357ac0cfea04663786a612ba1eaba9702bef25227a794b52890dd8bcd692e"
REG_NAME="kind-registry"
REG_PORT="5001"
REG_IMAGE="registry:2.8.3"
# The configuration script writes this ConfigMap when it is complete.
SETUP_MARKER="dynamos-dev-setup"

KIND_VERSION="v0.23.0"
KUBECTL_VERSION="v1.30.0"
HELM_VERSION="v3.16.4"
TILT_VERSION="0.37.9"
LINKERD_VERSION="edge-25.8.1"

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

container_state() {
    local state
    if state="$(docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null)"; then
        echo "${state}"
    else
        echo "missing"
    fi
}

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------
check_host() {
    for tool in docker curl tar openssl; do
        command -v "${tool}" >/dev/null || die "'${tool}' is not installed."
    done
    docker info >/dev/null 2>&1 || die "Docker does not run, or you cannot use it without sudo."
}

platform() {
    case "$(uname -s)" in
        Linux) OS="linux" ;;
        Darwin) OS="darwin" ;;
        *) die "Unsupported OS: $(uname -s)" ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        *) die "Unsupported CPU: $(uname -m)" ;;
    esac
}

download() {  # url target
    curl -fsSL --retry 3 -o "$2" "$1" || die "Download failed: $1"
}

install_tools() {
    platform
    mkdir -p "${BIN_DIR}"
    local tmp
    tmp="$(mktemp -d)"
    # shellcheck disable=SC2064
    trap "rm -rf '${tmp}'" RETURN

    if [[ ! -x "${BIN_DIR}/kind" ]]; then
        echo "  kind ${KIND_VERSION}"
        download "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-${OS}-${ARCH}" "${BIN_DIR}/kind"
    fi
    if [[ ! -x "${BIN_DIR}/kubectl" ]]; then
        echo "  kubectl ${KUBECTL_VERSION}"
        download "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${OS}/${ARCH}/kubectl" "${BIN_DIR}/kubectl"
    fi
    if [[ ! -x "${BIN_DIR}/helm" ]]; then
        echo "  helm ${HELM_VERSION}"
        download "https://get.helm.sh/helm-${HELM_VERSION}-${OS}-${ARCH}.tar.gz" "${tmp}/helm.tgz"
        tar -xzf "${tmp}/helm.tgz" -C "${tmp}"
        mv "${tmp}/${OS}-${ARCH}/helm" "${BIN_DIR}/helm"
    fi
    if [[ ! -x "${BIN_DIR}/tilt" ]]; then
        echo "  tilt ${TILT_VERSION}"
        local tilt_os="${OS/darwin/mac}" tilt_arch="${ARCH/amd64/x86_64}"
        download "https://github.com/tilt-dev/tilt/releases/download/v${TILT_VERSION}/tilt.${TILT_VERSION}.${tilt_os}.${tilt_arch}.tar.gz" "${tmp}/tilt.tgz"
        tar -xzf "${tmp}/tilt.tgz" -C "${tmp}" tilt
        mv "${tmp}/tilt" "${BIN_DIR}/tilt"
    fi
    if [[ ! -x "${BIN_DIR}/linkerd" ]]; then
        echo "  linkerd ${LINKERD_VERSION}"
        local suffix="${OS}-${ARCH}"
        [[ "${OS}-${ARCH}" == "darwin-amd64" ]] && suffix="darwin"
        download "https://github.com/linkerd/linkerd2/releases/download/${LINKERD_VERSION}/linkerd2-cli-${LINKERD_VERSION}-${suffix}" "${BIN_DIR}/linkerd"
    fi
    chmod +x "${BIN_DIR}"/*
}

# ---------------------------------------------------------------------------
# Registry and cluster
# ---------------------------------------------------------------------------
start_registry() {
    case "$(container_state "${REG_NAME}")" in
        running) ;;
        missing)
            echo "  Create registry ${REG_NAME} (localhost:${REG_PORT})"
            # No restart policy: the registry does not start after a reboot.
            docker run -d -p "127.0.0.1:${REG_PORT}:5000" --network bridge \
                --name "${REG_NAME}" "${REG_IMAGE}" >/dev/null ;;
        *)
            echo "  Start registry ${REG_NAME}"
            docker start "${REG_NAME}" >/dev/null ;;
    esac
}

create_cluster() {
    echo "  Create kind cluster ${CLUSTER}"
    # containerd reads the registry hosts from /etc/containerd/certs.d.
    cat <<EOF | kind create cluster --name "${CLUSTER}" --image "${NODE_IMAGE}" --wait 120s --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
EOF
    # localhost:5001 in the node is the registry container.
    local dir="/etc/containerd/certs.d/localhost:${REG_PORT}"
    docker exec "${NODE}" mkdir -p "${dir}"
    echo "[host.\"http://${REG_NAME}:5000\"]" | docker exec -i "${NODE}" cp /dev/stdin "${dir}/hosts.toml"
    # Tilt reads this ConfigMap and pushes its images to the registry.
    cat <<EOF | kubectl --context "${CONTEXT}" apply -f - >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REG_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
}

start_cluster() {
    case "$(container_state "${NODE}")" in
        running) echo "  Cluster ${CLUSTER} runs" ;;
        missing) create_cluster ;;
        *)
            echo "  Start cluster ${CLUSTER}"
            docker start "${NODE}" >/dev/null ;;
    esac
    # The node and the registry must be on the same Docker network.
    if [[ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${REG_NAME}")" == "null" ]]; then
        docker network connect kind "${REG_NAME}"
    fi
    kind export kubeconfig --name "${CLUSTER}" >/dev/null 2>&1
    kubectl config use-context "${CONTEXT}" >/dev/null
    echo "  Wait for the Kubernetes API"
    for _ in $(seq 1 60); do
        kubectl --context "${CONTEXT}" get --raw /readyz >/dev/null 2>&1 && break
        sleep 2
    done
    kubectl --context "${CONTEXT}" wait --for=condition=Ready node --all --timeout=180s >/dev/null
}

# ---------------------------------------------------------------------------
# Infrastructure (first time only)
# ---------------------------------------------------------------------------
setup_done() {
    kubectl --context "${CONTEXT}" get configmap "${SETUP_MARKER}" -n kube-public >/dev/null 2>&1
}

# After "docker start", the pods keep their old status ("Unknown") until the
# kubelet starts them again. Thus "rollout status" is not sufficient here:
# wait until no pod is Unknown and all pods (not Jobs) are ready.
wait_pods() {
    echo "  Wait for all pods (about 1-2 min after a restart)"
    local summary
    for _ in $(seq 1 60); do
        summary="$(kubectl --context "${CONTEXT}" get pods -A --no-headers 2>/dev/null | awk '
            $4 == "Completed" { next }
            { split($3, r, "/"); total++; if ($4 == "Running" && r[1] == r[2]) ok++ }
            END { printf "%d %d", ok, total }')"
        read -r ok total <<<"${summary}"
        if [[ "${total}" -gt 0 && "${ok}" -eq "${total}" ]]; then
            printf '\r  All %s pods are ready\n' "${total}"
            return
        fi
        printf '\r  Ready: %s/%s ' "${ok}" "${total}"
        sleep 5
    done
    echo
    echo "WARNING: not all pods are ready after 5 min. Check: kubectl get pods -A"
}

install_infra() {
    if setup_done; then
        echo "  Infrastructure is installed (delete with: ./dynamos-dev.sh reset)"
        wait_pods
        return
    fi
    # The configuration script makes this file again on each run. A run in
    # the dev container (as root) can leave it owned by root, and then the
    # script cannot write it. The directory belongs to the user, thus "rm"
    # works.
    rm -f "${REPO_ROOT}/configuration/k8s_service_files/definitions.json"
    echo "  Run configuration/dynamos-configuration.sh (5-10 min)"
    DYNAMOS_ROOT="${REPO_ROOT}" DYNAMOS_HOST_ROOT="${REPO_ROOT}" AUTH_KEYS_DIR="${AUTH_KEYS_DIR}" \
        "${REPO_ROOT}/configuration/dynamos-configuration.sh"
    kubectl --context "${CONTEXT}" create configmap "${SETUP_MARKER}" -n kube-public \
        --from-literal=completed="$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null
}

# The services do not start without the "dynamos-auth" Secrets. The
# script is idempotent, so a cluster made before the auth change gets them
# on the next "up".
install_auth() {
    AUTH_KEYS_DIR="${AUTH_KEYS_DIR}" KUBE_CONTEXT="${CONTEXT}" "${REPO_ROOT}/scripts/auth-secrets.sh"
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_up() {
    check_host
    log "Tools (.dev/bin)"
    install_tools
    log "Registry and cluster"
    start_registry
    start_cluster
    log "Infrastructure"
    install_infra
    log "Auth keys and Secrets"
    install_auth

    local tilt_cmd=".dev/bin/tilt up"
    PATH="${USER_PATH}" command -v tilt >/dev/null 2>&1 && tilt_cmd="tilt up"
    log "Ready. Next step:"
    echo "    ${tilt_cmd}"
    echo "  Dev auth tokens for curl and scripts: eval \"\$(./dynamos-dev.sh tokens)\""
    echo "  When you are done: Ctrl+C in Tilt, then ./dynamos-dev.sh down"
}

cmd_tokens() {
    AUTH_KEYS_DIR="${AUTH_KEYS_DIR}" "${REPO_ROOT}/scripts/auth-secrets.sh" --print
}

cmd_down() {
    if pgrep -f "tilt up" >/dev/null 2>&1; then
        echo "WARNING: Tilt still runs. Stop it with Ctrl+C in its terminal."
    fi
    for c in "${NODE}" "${REG_NAME}"; do
        if [[ "$(container_state "${c}")" == "running" ]]; then
            echo "  Stop ${c}"
            docker stop "${c}" >/dev/null
        fi
    done
    echo "  Stopped. Start again with: ./dynamos-dev.sh up"
}

cmd_reset() {
    if pgrep -f "tilt up" >/dev/null 2>&1; then
        die "Tilt still runs. Stop it with Ctrl+C first."
    fi
    echo "This deletes the kind cluster '${CLUSTER}' (all its data) and the registry '${REG_NAME}'."
    if [[ "${1:-}" != "-y" ]]; then
        read -r -p "Continue? [y/N] " answer
        [[ "${answer}" == "y" || "${answer}" == "Y" ]] || { echo "Cancelled."; exit 0; }
    fi
    install_tools >/dev/null
    kind delete cluster --name "${CLUSTER}" || true
    docker rm -f "${REG_NAME}" >/dev/null 2>&1 || true
    echo "  Deleted. Create again with: ./dynamos-dev.sh up"
}

cmd_status() {
    printf '  %-25s %s\n' "${NODE}" "$(container_state "${NODE}")" "${REG_NAME}" "$(container_state "${REG_NAME}")"
    if [[ "$(container_state "${NODE}")" == "running" ]] && setup_done; then
        echo "  Infrastructure: installed"
    fi
    pgrep -f "tilt up" >/dev/null 2>&1 && echo "  Tilt: runs" || echo "  Tilt: stopped"
}

case "${1:-}" in
    up) cmd_up ;;
    down) cmd_down ;;
    reset) shift; cmd_reset "$@" ;;
    status) cmd_status ;;
    tokens) cmd_tokens ;;
    *)
        sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
        exit 1 ;;
esac
