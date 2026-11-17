#!/usr/bin/env bash
# Create the kind cluster "dynamos" with a local image registry.
#
# The Tilt dev loop pushes the job microservice images (sql-*, sidecar) to
# this registry. The agent then pulls them from localhost:5001.
#
# Based on the kind local registry guide:
#   https://kind.sigs.k8s.io/docs/user/local-registry/
#
# Usage:
#   scripts/kind-with-registry.sh
#   configuration/dynamos-configuration.sh   # (in the dev container)
#   tilt up
#
# The script does not delete a cluster. If the cluster "dynamos" exists,
# delete it first: kind delete cluster --name dynamos
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-dynamos}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.30.0@sha256:047357ac0cfea04663786a612ba1eaba9702bef25227a794b52890dd8bcd692e}"
REG_NAME="kind-registry"
REG_PORT="5001"
REG_IMAGE="registry:2.8.3"

if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
    echo "Cluster '${CLUSTER_NAME}' exists. Delete it first:"
    echo "  kind delete cluster --name ${CLUSTER_NAME}"
    exit 1
fi

# 1. Start the registry container (if it does not run).
if [ "$(docker inspect -f '{{.State.Running}}' "${REG_NAME}" 2>/dev/null || true)" != "true" ]; then
    docker rm -f "${REG_NAME}" >/dev/null 2>&1 || true
    docker run -d --restart=always -p "127.0.0.1:${REG_PORT}:5000" \
        --network bridge --name "${REG_NAME}" "${REG_IMAGE}"
fi

# 2. Create the cluster. containerd reads registry hosts from certs.d.
cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --image "${NODE_IMAGE}" --wait 120s --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
EOF

# 3. Tell each node that localhost:5001 is the registry container.
REGISTRY_DIR="/etc/containerd/certs.d/localhost:${REG_PORT}"
for node in $(kind get nodes --name "${CLUSTER_NAME}"); do
    docker exec "${node}" mkdir -p "${REGISTRY_DIR}"
    cat <<EOF | docker exec -i "${node}" cp /dev/stdin "${REGISTRY_DIR}/hosts.toml"
[host."http://${REG_NAME}:5000"]
EOF
done

# 4. Connect the registry to the kind network (if it is not connected).
if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${REG_NAME}")" = 'null' ]; then
    docker network connect "kind" "${REG_NAME}"
fi

# 5. Publish the registry location. Tilt reads this ConfigMap and then
#    pushes its images to the registry (not "kind load").
cat <<EOF | kubectl --context "kind-${CLUSTER_NAME}" apply -f -
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

echo "Cluster '${CLUSTER_NAME}' is ready with registry localhost:${REG_PORT}."
