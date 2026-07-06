#!/usr/bin/env bash
# Start and keep-alive the port-forwards needed for local DYNAMOS development.
# Run inside the dev container. Press Ctrl+C to stop everything.
set -uo pipefail

cleanup() {
    echo -e "\nStopping all port-forwards..."
    kill 0 2>/dev/null
    exit 0
}
trap cleanup INT TERM

keepalive() {
    local name="$1"; shift
    while true; do
        echo "[pf] ${name}: connecting..."
        kubectl port-forward "$@" 2>&1 | sed "s/^/[${name}] /" || true
        echo "[pf] ${name}: connection lost — retrying in 3s..."
        sleep 3
    done
}

keepalive nginx -n ingress svc/nginx-nginx-ingress-controller 80:80   &
keepalive etcd  -n core    svc/etcd                           2379:2379 &

echo ""
echo "[pf] Port-forwards active. Press Ctrl+C to stop."
echo "     NGINX → :80    (browser: http://api-gateway.api-gateway.svc.cluster.local)"
echo "     etcd  → :2379  (export ETCDCTL_ENDPOINTS=127.0.0.1:2379)"
echo ""
wait
