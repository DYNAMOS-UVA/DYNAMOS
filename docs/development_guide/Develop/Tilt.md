# Tilt dev loop

[Tilt](https://tilt.dev) builds the DYNAMOS Go services from your local source and deploys them into the local kind cluster. When you save a file, Tilt builds the image again and restarts the related pods.

## What Tilt manages

| Tilt does | Tilt does not |
|-----------|---------------|
| Builds these images from `go/`: `orchestrator`, `policy-enforcer`, `sidecar`, `agent`, `api-gateway`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector` | Install the infrastructure (namespaces, RabbitMQ, etcd, Linkerd, NGINX, Prometheus, `core` chart) |
| Deploys these charts: `orchestrator`, `agents`, `thirdparty`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector`, `api-gateway` | Build `sql-graph` (it is in `microservices.json`, but it has no source code) |
| With a local registry only: builds the job images `sql-query`, `sql-algorithm`, `sql-anonymize`, `sql-aggregate`, `sql-test` and `sidecar` | |

## Job microservices and the local registry

The agent creates the job microservices at runtime (`go/cmd/agent/deploy_job.go`). It makes the image names from environment variables:

- `$MICROSERVICE_REPOSITORY_NAME/<name>:$<NAME>_TAG` (for example `SQL-QUERY_TAG`)
- `$SIDECAR_REPOSITORY_NAME/sidecar:$SIDECAR_TAG`

The pull policy is `Always`. Thus the node must pull each job image from a registry. An image that `kind load` puts on the node is not used.

When the cluster has a local registry, Tilt does these steps:

1. Builds the job images and pushes them as `localhost:5001/<name>:dev` (resources with the label `job-images`).
2. Adds the environment variables above to the agent Deployments `uva`, `vu` and `surf`, with the values `localhost:5001` and `dev`. Only the YAML that Tilt deploys changes. The charts do not change.
3. Each new job pulls the newest `:dev` image.

Without a local registry, Tilt does not do these steps, and the jobs pull the images from Docker Hub (`dynamos1`).

To create the cluster with a local registry (this deletes all data in the cluster):

```bash
kind delete cluster --name dynamos
scripts/kind-with-registry.sh
```

Then run `configuration/dynamos-configuration.sh` in the dev container. The host must have the `linkerd` CLI, or you must use the dev container:

```bash
docker run --rm --network host -v "$PWD:/workspace" -v "$HOME/.kube:/root/.kube" \
  -v /var/run/docker.sock:/var/run/docker.sock -e DYNAMOS_HOST_ROOT="$PWD" \
  -w /workspace dynamos-dev ./configuration/dynamos-configuration.sh
```

## Prerequisites

- `tilt`, `kind`, `kubectl`, `helm` and `docker` on your `PATH`.
- `python3` with `setuptools` and `wheel` (to build the `sql-query` image).
- The kind cluster `dynamos` is running.
- You ran `configuration/dynamos-configuration.sh` one time on this cluster.

The Tiltfile stops if the current kube context is not `kind-dynamos`. This prevents a deployment into a different cluster.

If you recreate the cluster while Tilt runs, stop Tilt and start it again. Tilt keeps the API server address of the old cluster.

## Start

```bash
kubectl config use-context kind-dynamos
tilt up
```

Open the Tilt UI at http://localhost:10350. The UI groups the resources with these labels: `core-services`, `agents`, `dsp`, `job-images`, `tools`.

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

- Each Go image uses `go/Dockerfile.tilt`. The runtime layout is the same as `go/Dockerfile` (Alpine, binary at `/app/<name>`). BuildKit cache mounts keep the Go module and build caches, thus a rebuild after a small change is fast.
- Tilt watches only `go/go.mod`, `go/go.sum`, `go/pkg/` and `go/cmd/<name>/`. A change in `go/pkg/` builds all images again. Tilt ignores changes to `*_test.go` files.
- Tilt renders the charts with `helm template` and the same `values.yaml` files as `dynamos-configuration.sh`. Tilt then replaces the `dynamos1/<service>` images with the local builds. With a local registry, Tilt pushes the images to it. Without a registry, Tilt loads them into kind (`kind load`).
- Tilt applies the rendered resources with `kubectl apply`, not `helm upgrade`. Thus `helm list` continues to show the release revision from the last time you ran the configuration script.
