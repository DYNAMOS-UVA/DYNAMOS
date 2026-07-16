#!/bin/bash

set -e

# ---------------------------------------------------------------------------
# Environment
# ---------------------------------------------------------------------------
# Load .env from the repo root if present (works both on the host and inside
# the dev container where the repo is mounted at /workspace).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -f "${REPO_ROOT}/.env" ]]; then
    set -a
    # shellcheck source=/dev/null
    source "${REPO_ROOT}/.env"
    set +a
fi

# DYNAMOS_ROOT      – path to the repo inside the current shell (charts, config).
#                    Defaults to /workspace (the dev-container mount point).
# DYNAMOS_HOST_ROOT – path to the repo as seen by the Kubernetes node (host
#                    machine). Only differs from DYNAMOS_ROOT when running from
#                    inside the dev container. Loaded from .env automatically,
#                    or set manually before running:
#   export DYNAMOS_HOST_ROOT=/home/youruser/Development/Go/DYNAMOS
echo "Setting up paths..."
DYNAMOS_ROOT="${DYNAMOS_ROOT:-/workspace}"
DYNAMOS_HOST_ROOT="${DYNAMOS_HOST_ROOT:-${DYNAMOS_ROOT}}"

if [[ "${DYNAMOS_HOST_ROOT}" == "/workspace" ]]; then
    echo "WARNING: DYNAMOS_HOST_ROOT is '/workspace'. If running inside the dev"
    echo "         container, set DYNAMOS_HOST_ROOT to the repo path on the HOST"
    echo "         machine (e.g. via .env) so Kubernetes hostPath mounts work."
fi

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------
charts_path="${DYNAMOS_ROOT}/charts"
core_chart="${charts_path}/core"
namespace_chart="${charts_path}/namespaces"
orchestrator_chart="${charts_path}/orchestrator"
agents_chart="${charts_path}/agents"
catalog_service_chart="${charts_path}/catalog-service"
dsp_connector_chart="${charts_path}/dsp-connector"
ttp_chart="${charts_path}/thirdparty"
api_gw_chart="${charts_path}/api-gateway"

config_path="${DYNAMOS_ROOT}/configuration"
k8s_service_files="${config_path}/k8s_service_files"
etcd_launch_files="${config_path}/etcd_launch_files"

rabbit_definitions_file="${k8s_service_files}/definitions.json"
example_definitions_file="${k8s_service_files}/definitions_example.json"

# ---------------------------------------------------------------------------
# Kubernetes cluster
# ---------------------------------------------------------------------------
echo "Checking Kubernetes cluster..."
if ! kubectl cluster-info &>/dev/null; then
    echo "  No reachable cluster found. Creating kind cluster 'dynamos'..."
    kind create cluster --name dynamos --wait 60s
else
    echo "  Cluster reachable."
fi

# ---------------------------------------------------------------------------
# RabbitMQ password
# ---------------------------------------------------------------------------
cp "$example_definitions_file" "$rabbit_definitions_file"
echo "definitions_example.json copied over definitions.json to ensure a clean file"

echo "Generating RabbitMQ password..."
rabbit_pw=$(openssl rand -hex 16)

# Use the RabbitCtl to make a special hash of that password:
hashed_pw=$($SUDO docker run --rm rabbitmq:3-management rabbitmqctl hash_password $rabbit_pw)
actual_hash=$(echo "$hashed_pw" | cut -d $'\n' -f2)

echo "Replacing tokens..."
cp ${k8s_service_files}/definitions_example.json ${rabbit_definitions_file}

# The Rabbit Hashed password needs to be in definitions.json file, that is the configuration for RabbitMQ
if [[ "$OSTYPE" == "darwin"* ]]; then
    sed -i '' "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
else
    sed -i "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
fi

# ---------------------------------------------------------------------------
# Helm installs
# ---------------------------------------------------------------------------
echo "Installing namespaces..."
helm upgrade -i -f ${namespace_chart}/values.yaml namespaces ${namespace_chart} --set secret.password=${rabbit_pw}

echo "Preparing PVC"
{
    cd ${DYNAMOS_ROOT}/configuration
    ./fill-rabbit-pvc.sh
}

echo "Installing Prometheus..."
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade -i -f "${core_chart}/prometheus-values.yaml" prometheus prometheus-community/prometheus

echo "Installing NGINX..."
# Drop the controller Deployment first so a stale kubectl-patch field owner
# doesn't conflict with helm's server-side apply.
kubectl delete deployment nginx-nginx-ingress-controller -n ingress --ignore-not-found
helm upgrade -i -f "${core_chart}/ingress-values.yaml" nginx oci://ghcr.io/nginxinc/charts/nginx-ingress -n ingress --version 0.18.0

echo "Re-enabling NGINX snippets..."
if ! kubectl get deployment nginx-nginx-ingress-controller -n ingress \
    -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- '-enable-snippets=true'; then
  kubectl patch deployment nginx-nginx-ingress-controller -n ingress \
    --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"-enable-snippets=true"}]'
else
  echo "Snippets already enabled, skipping."
fi

echo "Installing Gateway API CRDs (required by Linkerd)..."
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

if kubectl get namespace linkerd &>/dev/null; then
    echo "Linkerd already installed — upgrading..."
    linkerd upgrade --crds | kubectl apply -f -
    linkerd upgrade --set proxyInit.runAsRoot=true | kubectl apply -f -
else
    echo "Installing Linkerd CRDs..."
    linkerd install --crds | kubectl apply -f -
    echo "Installing Linkerd control plane..."
    linkerd install --set proxyInit.runAsRoot=true | kubectl apply -f -
fi
linkerd check

echo "Installing/upgrading Linkerd Jaeger..."
linkerd jaeger install | kubectl apply -f -

echo "Installing DYNAMOS core..."
helm upgrade -i -f ${core_chart}/values.yaml core ${core_chart} --set hostPath=${DYNAMOS_HOST_ROOT}

# Sync the RabbitMQ normal_user password to match the Kubernetes secret.
# Without this, a pod restart after a second script run would pick up the new
# secret value but RabbitMQ would still have the old hash → crash loop.
echo "Syncing RabbitMQ normal_user password..."
echo "  Waiting for RabbitMQ to be ready..."
kubectl rollout status deployment/rabbitmq -n core --timeout=120s
kubectl exec -n core deployment/rabbitmq -c rabbitmq -- \
    rabbitmqctl change_password normal_user "${rabbit_pw}"
echo "  RabbitMQ password synced."

sleep 3

echo "Installing orchestrator layer..."
helm upgrade -i -f "${orchestrator_chart}/values.yaml" orchestrator ${orchestrator_chart}

sleep 1

echo "Installing agents layer..."
helm upgrade -i -f "${agents_chart}/values.yaml" agents ${agents_chart}

sleep 1

echo "Installing catalog-service layer..."
helm upgrade -i -f "${catalog_service_chart}/values.yaml" catalog-service ${catalog_service_chart}

sleep 1

echo "Installing dsp-connector layer..."
helm upgrade -i -f "${dsp_connector_chart}/values.yaml" dsp-connector ${dsp_connector_chart}

sleep 1

echo "Installing thirdparty layer..."
helm upgrade -i -f "${ttp_chart}/values.yaml" surf ${ttp_chart}

sleep 1

echo "Installing api gateway..."
helm upgrade -i -f "${api_gw_chart}/values.yaml" api-gateway ${api_gw_chart}

echo ""
echo "Finished setting up DYNAMOS"
echo "  Run ./pf.sh inside the dev container to start port-forwards."

exit 0
