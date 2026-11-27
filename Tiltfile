# -*- mode: Python -*-
#
# Tilt dev loop for DYNAMOS.
#
#   ./dynamos-dev.sh up     # cluster, registry, infrastructure
#   tilt up                 # this file: build and deploy the services
#
# Tilt builds the images from local source, pushes them to the local
# registry (localhost:5001) and deploys the DYNAMOS application Helm charts.
# See docs/development_guide/Develop/Tilt.md.
#
# Deploy only some charts: tilt up -- --only orchestrator --only api-gateway

# Use the pinned tools from ./dynamos-dev.sh (helm, kubectl) first.
os.putenv('PATH', os.path.abspath('.dev/bin') + ':' + os.getenv('PATH', ''))

# Deploy into the DYNAMOS cluster only. A second kind cluster (for example
# kind-mvd) can be the current context (issue #94).
CONTEXT = 'kind-dynamos'
if k8s_context() != CONTEXT:
    fail('Current kube context is "%s". Run ./dynamos-dev.sh up first.' % k8s_context())
allow_k8s_contexts(CONTEXT)

registry = str(local(
    'kubectl --context %s get configmap local-registry-hosting -n kube-public --ignore-not-found -o name' % CONTEXT,
    quiet=True, echo_off=True)).strip()
if not registry:
    fail('The cluster has no local registry. Run ./dynamos-dev.sh up first.')

# ---------------------------------------------------------------------------
# Charts
# ---------------------------------------------------------------------------
# Chart -> Helm release name (same as dynamos-configuration.sh), the Go
# images that the chart uses, and its workloads (with a UI label).
CHARTS = {
    'orchestrator': ('orchestrator', ['orchestrator', 'policy-enforcer', 'sidecar'],
                     {'orchestrator': 'core-services', 'policy-enforcer': 'core-services'}),
    'api-gateway': ('api-gateway', ['api-gateway', 'sidecar'], {'api-gateway': 'core-services'}),
    'agents': ('agents', ['agent', 'sidecar'], {'uva': 'agents', 'vu': 'agents'}),
    'thirdparty': ('surf', ['agent', 'sidecar'], {'surf': 'agents'}),
}
for chart in ['catalog-service', 'negotiation-service', 'transfer-process-service', 'dsp-connector']:
    CHARTS[chart] = (chart, [chart], {chart + '-' + p: 'dsp' for p in ['uva', 'vu', 'surf']})

config.define_string_list('only', usage='Charts to deploy (default: all)')
selected = config.parse().get('only', []) or CHARTS.keys()
for chart in selected:
    if chart not in CHARTS:
        fail('Unknown chart "%s". Use one of: %s' % (chart, ', '.join(CHARTS.keys())))

# ---------------------------------------------------------------------------
# Go service images (in the charts)
# ---------------------------------------------------------------------------
GO_DOCKERFILE = 'go/Dockerfile.tilt'
GO_DEPS = ['go.mod', 'go.sum', 'pkg']

built = []
for chart in selected:
    for name in CHARTS[chart][1]:
        if name in built:
            continue
        built.append(name)
        docker_build(
            'dynamos1/' + name,
            context='go',
            dockerfile=GO_DOCKERFILE,
            build_args={'NAME': name},
            only=GO_DEPS + ['cmd/' + name],
            ignore=['**/*_test.go'],
        )

# ---------------------------------------------------------------------------
# Job microservice images
# ---------------------------------------------------------------------------
# The agent creates the jobs at runtime (go/cmd/agent/deploy_job.go) with:
#   image   = $MICROSERVICE_REPOSITORY_NAME/<name>:$<NAME>_TAG
#   sidecar = $SIDECAR_REPOSITORY_NAME/sidecar:$SIDECAR_TAG
# and pull policy "Always". Tilt pushes the images as localhost:5001/<name>:dev
# and sets these variables on the agents. Each new job pulls the newest image.
JOB_REGISTRY = 'localhost:5001'
JOB_TAG = 'dev'
GO_JOBS = ['sql-algorithm', 'sql-anonymize', 'sql-aggregate', 'sql-test', 'sidecar']
PY_JOBS = ['sql-query']

def job_ref(name):
    return '%s/%s:%s' % (JOB_REGISTRY, name, JOB_TAG)

for name in GO_JOBS:
    local_resource(
        'job-image-' + name,
        cmd='docker build -q -f %s --build-arg NAME=%s -t %s go && docker push -q %s'
            % (GO_DOCKERFILE, name, job_ref(name), job_ref(name)),
        deps=['go/' + d for d in GO_DEPS + ['cmd/' + name]] + [GO_DOCKERFILE],
        ignore=['**/*_test.go'],
        labels=['job-images'],
    )
for name in PY_JOBS:
    local_resource(
        'job-image-' + name,
        cmd='scripts/tilt-build-sql-query.sh ' + job_ref(name),
        deps=['python/' + name, 'python/dynamos-python-lib/dynamos',
              'python/dynamos-python-lib/protofiles', 'python/dynamos-python-lib/setup.py',
              'python/Dockerfile', 'scripts/tilt-build-sql-query.sh'],
        ignore=['**/__pycache__'],
        labels=['job-images'],
    )

JOB_ENV = [
    {'name': 'MICROSERVICE_REPOSITORY_NAME', 'value': JOB_REGISTRY},
    {'name': 'SIDECAR_REPOSITORY_NAME', 'value': JOB_REGISTRY},
] + [
    # Same key as getMicroserviceTag(): strings.ToUpper(name) + "_TAG".
    {'name': name.upper() + '_TAG', 'value': JOB_TAG} for name in GO_JOBS + PY_JOBS
]

def add_job_env(objects):
    for obj in objects:
        if obj.get('kind') != 'Deployment':
            continue
        name = obj['metadata']['name']
        for container in obj['spec']['template']['spec']['containers']:
            # The agent container has the same name as its Deployment.
            if name in ['uva', 'vu', 'surf'] and container['name'] == name:
                container['env'] = (container.get('env') or []) + JOB_ENV
    return objects

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------
# Auth: the charts mount the Secret "dynamos-auth" from
# scripts/auth-secrets.sh (./dynamos-dev.sh up runs it). The Tilt images are
# built with -tags authdev, so these charts also accept the static dev
# tokens. Show them with: ./dynamos-dev.sh tokens
DEV_TOKEN_CHARTS = ['orchestrator', 'api-gateway']

for chart in selected:
    release, _, workloads = CHARTS[chart]
    sets = ['auth.devToken.enabled=true'] if chart in DEV_TOKEN_CHARTS else []
    rendered = helm('charts/' + chart, name=release, values=['charts/%s/values.yaml' % chart], set=sets)
    if chart in ['agents', 'thirdparty']:
        rendered = encode_yaml_stream(add_job_env(decode_yaml_stream(rendered)))
    k8s_yaml(rendered)
    for workload, label in workloads.items():
        k8s_resource(workload, labels=[label])

# Port-forward to the NGINX ingress. Start it in the Tilt UI. It stops with Tilt.
# curl http://localhost:8080/api/v1/... -H 'Host: api-gateway.api-gateway.svc.cluster.local'
local_resource(
    'ingress-port-forward',
    serve_cmd='kubectl --context %s port-forward -n ingress svc/nginx-nginx-ingress-controller 8080:80' % CONTEXT,
    auto_init=False,
    trigger_mode=TRIGGER_MODE_MANUAL,
    labels=['tools'],
)
