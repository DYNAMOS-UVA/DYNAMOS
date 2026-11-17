# -*- mode: Python -*-
#
# Tilt dev loop for DYNAMOS.
#
# Scope:
#   - Tilt builds the Go service images from local source and deploys the
#     DYNAMOS application Helm charts into the kind cluster "dynamos".
#   - Tilt does NOT install the infrastructure. Run
#     configuration/dynamos-configuration.sh one time first (namespaces,
#     RabbitMQ, etcd, Linkerd, NGINX, Prometheus, core chart).
#   - Tilt does NOT build the job microservices (sql-*, sidecar in jobs).
#     The agent creates these jobs with "imagePullPolicy: Always", thus the
#     jobs continue to pull the images from Docker Hub (dynamos1).
#
# Usage:
#   tilt up                      # all services
#   tilt up -- --only orchestrator --only api-gateway
#   tilt down                    # remove the application resources
#
# See docs/development_guide/Develop/Tilt.md for more data.

# Make sure that Tilt deploys into the DYNAMOS cluster only. A second kind
# cluster (for example kind-mvd) can be the current context (issue #94).
EXPECTED_CONTEXT = 'kind-dynamos'
if k8s_context() != EXPECTED_CONTEXT:
    fail('Current kube context is "%s". Run: kubectl config use-context %s'
         % (k8s_context(), EXPECTED_CONTEXT))
allow_k8s_contexts(EXPECTED_CONTEXT)

# The charts use "<dockerArtifactAccount>/<service>:<branchNameTag>".
# Tilt replaces each image that has a docker_build() with the same name.
REPO = 'dynamos1'

# ---------------------------------------------------------------------------
# Optional arguments
# ---------------------------------------------------------------------------
config.define_string_list('only', usage='Helm charts to deploy (default: all)')
cfg = config.parse()

# Helm chart -> Helm release name used by dynamos-configuration.sh.
CHARTS = {
    'orchestrator': 'orchestrator',
    'agents': 'agents',
    'thirdparty': 'surf',
    'catalog-service': 'catalog-service',
    'negotiation-service': 'negotiation-service',
    'transfer-process-service': 'transfer-process-service',
    'dsp-connector': 'dsp-connector',
    'api-gateway': 'api-gateway',
}

selected = cfg.get('only', [])
for name in selected:
    if name not in CHARTS:
        fail('Unknown chart "%s". Use one of: %s' % (name, ', '.join(CHARTS.keys())))
if not selected:
    selected = CHARTS.keys()

# ---------------------------------------------------------------------------
# Image builds (Go)
# ---------------------------------------------------------------------------
# The same runtime layout as go/Dockerfile (alpine, binary at /app/<name>).
# The BuildKit cache mounts keep the Go module and build caches between
# builds, thus a rebuild after a small change is fast.
GO_DOCKERFILE = '''
# syntax=docker/dockerfile:1
FROM golang:1.22.5-bookworm AS builder
ARG NAME
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY pkg ./pkg
COPY cmd/${NAME} ./cmd/${NAME}
RUN --mount=type=cache,target=/go/pkg/mod \\
    --mount=type=cache,target=/root/.cache/go-build \\
    CGO_ENABLED=0 GOOS=linux go build -o /out/${NAME} ./cmd/${NAME}

FROM alpine:3.18.2
ARG NAME
ENV NAME=${NAME}
RUN apk --no-cache add ca-certificates
WORKDIR /app/
COPY --from=builder /out/${NAME} .
CMD ["/bin/sh", "-c", "/app/$NAME || ls -al /app"]
'''

def go_image(name):
    docker_build(
        '%s/%s' % (REPO, name),
        context='go',
        dockerfile_contents=GO_DOCKERFILE,
        build_args={'NAME': name},
        # Send and watch only the files that the build uses.
        only=['go.mod', 'go.sum', 'pkg', 'cmd/' + name],
        ignore=['**/*_test.go'],
    )

# Go images that each chart uses.
CHART_IMAGES = {
    'orchestrator': ['orchestrator', 'policy-enforcer', 'sidecar'],
    'agents': ['agent', 'sidecar'],
    'thirdparty': ['agent', 'sidecar'],
    'catalog-service': ['catalog-service'],
    'negotiation-service': ['negotiation-service'],
    'transfer-process-service': ['transfer-process-service'],
    'dsp-connector': ['dsp-connector'],
    'api-gateway': ['api-gateway', 'sidecar'],
}

images = []
for chart in selected:
    for image in CHART_IMAGES[chart]:
        if image not in images:
            images.append(image)
for image in images:
    go_image(image)

# ---------------------------------------------------------------------------
# Helm charts
# ---------------------------------------------------------------------------
# helm() renders the chart locally and Tilt applies the result. The values
# files are the same as in dynamos-configuration.sh.
for chart in selected:
    k8s_yaml(helm(
        'charts/' + chart,
        name=CHARTS[chart],
        values=['charts/%s/values.yaml' % chart],
    ))

# ---------------------------------------------------------------------------
# Tilt UI labels
# ---------------------------------------------------------------------------
LABELS = {
    'orchestrator': 'core-services',
    'policy-enforcer': 'core-services',
    'api-gateway': 'core-services',
    'uva': 'agents',
    'vu': 'agents',
    'surf': 'agents',
}
DSP_PREFIXES = ['catalog-service-', 'negotiation-service-',
                'transfer-process-service-', 'dsp-connector-']
DSP_WORKLOADS = []
for prefix in DSP_PREFIXES:
    for party in ['uva', 'vu', 'surf']:
        DSP_WORKLOADS.append(prefix + party)

def chart_workloads(chart):
    if chart == 'orchestrator':
        return ['orchestrator', 'policy-enforcer']
    if chart == 'agents':
        return ['uva', 'vu']
    if chart == 'thirdparty':
        return ['surf']
    if chart == 'api-gateway':
        return ['api-gateway']
    return [w for w in DSP_WORKLOADS if w.startswith(chart + '-')]

for chart in selected:
    for workload in chart_workloads(chart):
        k8s_resource(workload, labels=[LABELS.get(workload, 'dsp')])

# ---------------------------------------------------------------------------
# Port-forward to the NGINX ingress (manual start in the Tilt UI)
# ---------------------------------------------------------------------------
# Use: curl http://localhost:8080/api/v1/... -H 'Host: api-gateway.api-gateway.svc.cluster.local'
local_resource(
    'ingress-port-forward',
    serve_cmd='kubectl --context %s port-forward -n ingress svc/nginx-nginx-ingress-controller 8080:80'
              % EXPECTED_CONTEXT,
    auto_init=False,
    trigger_mode=TRIGGER_MODE_MANUAL,
    labels=['tools'],
)
