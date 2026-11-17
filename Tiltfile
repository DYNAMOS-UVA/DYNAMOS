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
#   - Job microservices (sql-*, sidecar in jobs): the agent creates these
#     jobs at runtime. If the cluster has a local registry
#     (scripts/kind-with-registry.sh), Tilt builds these images, pushes them
#     as localhost:5001/<name>:dev and tells the agents to use them.
#     Without a local registry, the jobs pull the images from Docker Hub.
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
# Image builds (Go services in the charts)
# ---------------------------------------------------------------------------
GO_DOCKERFILE = 'go/Dockerfile.tilt'

def go_image(name):
    docker_build(
        '%s/%s' % (REPO, name),
        context='go',
        dockerfile=GO_DOCKERFILE,
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
# Job microservice images (local registry only)
# ---------------------------------------------------------------------------
# The agent creates the job containers from environment variables
# (go/cmd/agent/deploy_job.go):
#   image = $MICROSERVICE_REPOSITORY_NAME/<name>:$<NAME>_TAG
#   sidecar = $SIDECAR_REPOSITORY_NAME/sidecar:$SIDECAR_TAG
# The pull policy is "Always". Thus each new job pulls the newest ":dev"
# image from the local registry. Tilt changes only the environment of the
# agent Deployments that it deploys. The charts do not change.
JOB_REGISTRY = 'localhost:5001'
JOB_TAG = 'dev'
GO_JOB_IMAGES = ['sql-algorithm', 'sql-anonymize', 'sql-aggregate', 'sql-test', 'sidecar']
PY_JOB_IMAGES = ['sql-query']
AGENT_DEPLOYMENTS = ['uva', 'vu', 'surf']

registry_found = str(local(
    'kubectl --context %s get configmap local-registry-hosting -n kube-public --ignore-not-found -o name'
    % EXPECTED_CONTEXT, quiet=True, echo_off=True)).strip() != ''
build_job_images = registry_found and ('agents' in selected or 'thirdparty' in selected)

if not registry_found:
    print('No local registry found. Jobs (sql-*) use the Docker Hub images.')
    print('To build them with Tilt, create the cluster with scripts/kind-with-registry.sh.')

def job_image_ref(name):
    return '%s/%s:%s' % (JOB_REGISTRY, name, JOB_TAG)

if build_job_images:
    for name in GO_JOB_IMAGES:
        local_resource(
            'job-image-' + name,
            cmd=('docker build -f %s --build-arg NAME=%s -t %s go && docker push %s'
                 % (GO_DOCKERFILE, name, job_image_ref(name), job_image_ref(name))),
            deps=['go/go.mod', 'go/go.sum', 'go/pkg', 'go/cmd/' + name, GO_DOCKERFILE],
            ignore=['**/*_test.go'],
            labels=['job-images'],
        )
    for name in PY_JOB_IMAGES:
        local_resource(
            'job-image-' + name,
            cmd='scripts/tilt-build-sql-query.sh %s' % job_image_ref(name),
            deps=['python/' + name, 'python/dynamos-python-lib/dynamos',
                  'python/dynamos-python-lib/protofiles', 'python/dynamos-python-lib/setup.py',
                  'python/Dockerfile', 'scripts/tilt-build-sql-query.sh'],
            ignore=['**/__pycache__'],
            labels=['job-images'],
        )

def job_env():
    env = [
        {'name': 'MICROSERVICE_REPOSITORY_NAME', 'value': JOB_REGISTRY},
        {'name': 'SIDECAR_REPOSITORY_NAME', 'value': JOB_REGISTRY},
    ]
    for name in GO_JOB_IMAGES + PY_JOB_IMAGES:
        # Same key as getMicroserviceTag(): strings.ToUpper(name) + "_TAG".
        env.append({'name': name.upper() + '_TAG', 'value': JOB_TAG})
    return env

def add_job_env(objects):
    for obj in objects:
        if obj.get('kind') != 'Deployment':
            continue
        name = obj['metadata']['name']
        if name not in AGENT_DEPLOYMENTS:
            continue
        for container in obj['spec']['template']['spec']['containers']:
            if container['name'] == name:
                container['env'] = (container.get('env') or []) + job_env()
    return objects

# ---------------------------------------------------------------------------
# Helm charts
# ---------------------------------------------------------------------------
# helm() renders the chart locally and Tilt applies the result. The values
# files are the same as in dynamos-configuration.sh.
for chart in selected:
    rendered = helm(
        'charts/' + chart,
        name=CHARTS[chart],
        values=['charts/%s/values.yaml' % chart],
    )
    if build_job_images and chart in ['agents', 'thirdparty']:
        rendered = encode_yaml_stream(add_job_env(decode_yaml_stream(rendered)))
    k8s_yaml(rendered)

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
