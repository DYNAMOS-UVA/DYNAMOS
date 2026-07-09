FROM golang:1.23-bookworm

# ── System packages ───────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    git curl wget unzip make \
    protobuf-compiler libprotobuf-dev \
    python3 python3-pip python3-venv \
    ca-certificates gnupg apt-transport-https lsb-release \
    openssl \
    && rm -rf /var/lib/apt/lists/*

# Trust any directory so 'git' works on the host-mounted /workspace volume
RUN git config --global --add safe.directory '*'

# ── Go protobuf / gRPC code generators (pinned to match go.mod) ──────────────
# protoc-gen-go version mirrors google.golang.org/protobuf v1.34.2 in go.mod.
# protoc-gen-go-grpc v1.3.0 is the last release that compiles under Go 1.23
# and generates stubs fully compatible with grpc v1.65.0 used in go.mod.
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

# ── Python gRPC tools + wheel builder ────────────────────────────────────────
ENV PIP_BREAK_SYSTEM_PACKAGES=1
RUN pip3 install grpcio-tools wheel setuptools

# ── Node.js LTS (for the Vue/web frontend) ───────────────────────────────────
RUN curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# ── Docker CLI (connects to the host daemon via the mounted socket) ───────────
RUN curl -fsSL https://download.docker.com/linux/debian/gpg \
        | gpg --dearmor -o /usr/share/keyrings/docker.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker.gpg] \
        https://download.docker.com/linux/debian $(lsb_release -cs) stable" \
        > /etc/apt/sources.list.d/docker.list && \
    apt-get update && apt-get install -y --no-install-recommends docker-ce-cli && \
    rm -rf /var/lib/apt/lists/*

# ── kubectl ───────────────────────────────────────────────────────────────────
RUN KUBECTL_VERSION=$(curl -sSL https://dl.k8s.io/release/stable.txt) && \
    curl -sSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
        -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

# ── Helm v4 ───────────────────────────────────────────────────────────────────
RUN curl -fsSL https://packages.buildkite.com/helm-linux/helm-debian/gpgkey \
        | gpg --dearmor | tee /usr/share/keyrings/helm.gpg > /dev/null && \
    echo "deb [signed-by=/usr/share/keyrings/helm.gpg] https://packages.buildkite.com/helm-linux/helm-debian/any/ any main" \
        | tee /etc/apt/sources.list.d/helm-stable-debian.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends helm && \
    rm -rf /var/lib/apt/lists/*

# ── Linkerd CLI (pinned version from readme) ──────────────────────────────────
RUN curl --proto '=https' --tlsv1.2 -sSfL https://run.linkerd.io/install-edge \
        | LINKERD2_VERSION=edge-25.8.1 sh
ENV PATH="/root/.linkerd2/bin:${PATH}"

# ── k9s (Kubernetes TUI) ──────────────────────────────────────────────────────
RUN K9S_VERSION=$(curl -sSL https://api.github.com/repos/derailed/k9s/releases/latest \
        | grep '"tag_name"' | cut -d'"' -f4) && \
    curl -sSL "https://github.com/derailed/k9s/releases/download/${K9S_VERSION}/k9s_Linux_amd64.tar.gz" \
        | tar -xz -C /usr/local/bin k9s

# ── etcdctl (pinned to match go.mod etcd v3.5.12) ────────────────────────────
RUN curl -sSL "https://github.com/etcd-io/etcd/releases/download/v3.5.12/etcd-v3.5.12-linux-amd64.tar.gz" \
        | tar -xz --strip-components=1 -C /usr/local/bin \
            etcd-v3.5.12-linux-amd64/etcdctl

WORKDIR /workspace

ENV GO111MODULE=on
ENV GOPATH=/go
ENV PATH="/go/bin:${PATH}"
