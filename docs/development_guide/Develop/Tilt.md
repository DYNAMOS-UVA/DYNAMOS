# Tilt dev loop

[Tilt](https://tilt.dev) builds the DYNAMOS Go services from your local source and deploys them into the local kind cluster. When you save a file, Tilt builds the image again and restarts the related pods.

## What Tilt manages

| Tilt does | Tilt does not |
|-----------|---------------|
| Builds these images from `go/`: `orchestrator`, `policy-enforcer`, `sidecar`, `agent`, `api-gateway`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector` | Install the infrastructure (namespaces, RabbitMQ, etcd, Linkerd, NGINX, Prometheus, `core` chart) |
| Deploys these charts: `orchestrator`, `agents`, `thirdparty`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector`, `api-gateway` | Build the job microservices (`sql-*`, `sql-query`) |

The agent creates the job microservices at runtime with `imagePullPolicy: Always`. Thus these jobs (and the sidecar in each job) continue to pull the images from Docker Hub (`dynamos1`).

## Prerequisites

- `tilt`, `kind`, `kubectl`, `helm` and `docker` on your `PATH`.
- The kind cluster `dynamos` is running.
- You ran `configuration/dynamos-configuration.sh` one time on this cluster.

The Tiltfile stops if the current kube context is not `kind-dynamos`. This prevents a deployment into a different cluster.

## Start

```bash
kubectl config use-context kind-dynamos
tilt up
```

Open the Tilt UI at http://localhost:10350. The UI groups the resources with these labels: `core-services`, `agents`, `dsp`, `tools`.

To deploy only some charts, give the chart names after `--`:

```bash
tilt up -- --only orchestrator --only api-gateway
```

## Send requests

Start the `ingress-port-forward` resource in the Tilt UI. It forwards `localhost:8080` to the NGINX ingress. Then send the request with the `Host` header:

```bash
curl http://localhost:8080/api/v1/requestApproval \
  -H 'Host: api-gateway.api-gateway.svc.cluster.local' \
  -H 'Content-Type: application/json' \
  --data-raw '{...}'
```

## Stop

- `Ctrl+C` stops Tilt. The pods continue to run with the last images that Tilt built.
- `tilt down` deletes the application resources that Tilt deployed. To get the Docker Hub images back, run `configuration/dynamos-configuration.sh` again.

## How it works

- Each Go image uses an inline Dockerfile in the `Tiltfile`. The runtime layout is the same as `go/Dockerfile` (Alpine, binary at `/app/<name>`). BuildKit cache mounts keep the Go module and build caches, thus a rebuild after a small change is fast.
- Tilt watches only `go/go.mod`, `go/go.sum`, `go/pkg/` and `go/cmd/<name>/`. A change in `go/pkg/` builds all images again. Tilt ignores changes to `*_test.go` files.
- Tilt renders the charts with `helm template` and the same `values.yaml` files as `dynamos-configuration.sh`. Tilt then replaces the `dynamos1/<service>` images with the local builds and loads them into kind (`kind load`).
- Tilt applies the rendered resources with `kubectl apply`, not `helm upgrade`. Thus `helm list` continues to show the release revision from the last time you ran the configuration script.
