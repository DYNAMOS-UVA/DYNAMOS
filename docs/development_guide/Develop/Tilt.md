# Local development with Tilt

Run DYNAMOS on your computer from your local source. When you save a file, [Tilt](https://tilt.dev) builds the image again and restarts the related pods.

## Requirements

- Docker (you must be able to use it without `sudo`)
- `curl`, `tar`, `openssl`
- About 8 GB of free memory for Docker

You do not have to install `kind`, `kubectl`, `helm`, `tilt` or `linkerd`. `./dynamos-dev.sh up` downloads pinned versions into `.dev/bin/` (ignored by git).

## Commands

| Step | Command | What it does |
|------|---------|--------------|
| 1. Start | `./dynamos-dev.sh up` | Downloads the tools, starts the local registry and the kind cluster, and installs the infrastructure. The first time takes 5-10 minutes. After `down`, about 2 minutes (it waits until all pods are ready). |
| 2. Develop | `tilt up` | Builds and deploys the services. Open http://localhost:10350. Use `.dev/bin/tilt up` if `tilt` is not on your `PATH`. |
| 3. Stop Tilt | `Ctrl+C` | Stops Tilt and its port-forward. The pods continue to run. |
| 4. Stop all | `./dynamos-dev.sh down` | Stops the cluster and registry containers. The data stays. After this step, nothing from DYNAMOS runs. |

Other commands:

- `./dynamos-dev.sh status`: shows the state of the containers and Tilt.
- `./dynamos-dev.sh reset`: deletes the cluster and the registry (all data). Use it to start again from a clean state. Then run `./dynamos-dev.sh up`.

The script uses only the kube context `kind-dynamos`. It does not change other clusters.

## Send requests

In the Tilt UI, start the `ingress-port-forward` resource. It forwards `localhost:8080` to the NGINX ingress:

```bash
curl http://localhost:8080/api/v1/requestApproval \
  -H 'Host: api-gateway.api-gateway.svc.cluster.local' \
  -H 'Content-Type: application/json' \
  --data-raw '{...}'
```

## What Tilt builds

| Group (Tilt label) | Images | How it is deployed |
|--------------------|--------|--------------------|
| `core-services`, `agents`, `dsp` | `orchestrator`, `policy-enforcer`, `api-gateway`, `agent`, `sidecar`, `catalog-service`, `negotiation-service`, `transfer-process-service`, `dsp-connector` | Tilt deploys the Helm charts in `charts/` with these images. |
| `job-images` | `sql-query`, `sql-algorithm`, `sql-anonymize`, `sql-aggregate`, `sql-test`, `sidecar` | Pushed as `localhost:5001/<name>:dev`. The agents start jobs with these images (see below). |

`sql-graph` is in `configuration/etcd_launch_files/microservices.json`, but it has no source code. Tilt cannot build it.

## How it works

- **Infrastructure:** `./dynamos-dev.sh up` runs `configuration/dynamos-configuration.sh` one time on a new cluster: namespaces, RabbitMQ, etcd, Linkerd, NGINX, Prometheus and the `core` chart. When it is complete, the script writes the ConfigMap `kube-public/dynamos-dev-setup`. If this ConfigMap exists, `up` does not run the configuration script again.
- **Registry:** the container `kind-registry` is a Docker registry at `localhost:5001`. The kind node uses it through `/etc/containerd/certs.d`. It has no restart policy, thus it does not start after a reboot.
- **Service images:** `go/Dockerfile.tilt` builds each Go service. The result is the same as `go/Dockerfile` (Alpine, binary at `/app/<name>`). BuildKit cache mounts keep the Go caches, thus a rebuild is fast. Tilt watches `go/go.mod`, `go/go.sum`, `go/pkg/` and `go/cmd/<name>/`. A change in `go/pkg/` builds all images again.
- **Job images:** the agent creates the job containers at runtime (`go/cmd/agent/deploy_job.go`) as `$MICROSERVICE_REPOSITORY_NAME/<name>:$<NAME>_TAG` with pull policy `Always`. Tilt sets these variables on the agent Deployments `uva`, `vu` and `surf` (only in the YAML that Tilt applies, not in the charts). Thus each new job pulls the newest `:dev` image. `scripts/tilt-build-sql-query.sh` builds `sql-query` with the same steps as `python/Makefile`, in a temporary directory.
- **Charts:** Tilt renders the charts with `helm template` and the same `values.yaml` files as `dynamos-configuration.sh`, and applies them with `kubectl apply`. Thus `helm list` shows the release revision from the configuration script.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Tilt shows `connection refused` to `127.0.0.1:<port>` | The cluster was recreated or restarted while Tilt ran. Stop Tilt and start it again. |
| `Current kube context is ...` | Run `./dynamos-dev.sh up`. It selects `kind-dynamos`. |
| Pods restart, sidecar shows `nil pointer` at `grpc_server.go` | RabbitMQ does not run. Run `kubectl get pods -n core`. If the infrastructure is damaged, run `./dynamos-dev.sh reset` and `./dynamos-dev.sh up`. |
| The first `up` stopped with an error | Run `./dynamos-dev.sh up` again. The configuration script is safe to run again. |
