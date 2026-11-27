#!/usr/bin/env bash
# Make the DYNAMOS auth keys and put them in Kubernetes Secrets.
#
#   scripts/auth-secrets.sh                Make missing keys, apply the Secrets.
#   scripts/auth-secrets.sh --print        Show the dev tokens as shell exports.
#   scripts/auth-secrets.sh rotate NAME    Replace a key without downtime.
#   scripts/auth-secrets.sh retire NAME    Stop trusting the old key of NAME.
#   scripts/auth-secrets.sh status         Show the keys and open rotations.
#
# NAME is a signing key (orchestrator, transfer-process-service) or a dev
# token (dev-user-token, dev-operator-token).
#
# Environment:
#   AUTH_KEYS_DIR        key directory (default: <repo>/.dev/auth, git-ignored)
#   KUBE_CONTEXT         kubectl context (default: the current context)
#   AUTH_ROTATION_GRACE  seconds between rotate and retire (default: 600)
#
# Without a command, the script does not replace a key that exists. Run it
# again at any time: it only adds what is missing.
#
# Each namespace gets one Secret "dynamos-auth" with only the files that
# its services need. The charts mount it at /etc/dynamos/auth.
#
#   namespace                 files
#   orchestrator              orchestrator.key.pem          signs job tokens
#                             dev-operator-token
#   uva, vu, surf             orchestrator.pub.pem          agents verify job tokens
#   transfer-process-service  transfer-process-service.key.pem
#   api-gateway               transfer-process-service.pub.pem
#                             dev-user-token
#
# A *.pub.pem file in a Secret can hold more than one public key. The
# verifiers accept a token that any of them signed. This is how a rotation
# works without downtime:
#
#   rotate  1. Make the next key. The verifiers trust the current and the
#              next key. Restart the verifiers.
#           2. The issuer signs with the next key. Restart the issuer.
#              The verifiers still trust the old key, so tokens that the
#              old key signed stay valid until they expire.
#   retire  3. After AUTH_ROTATION_GRACE (longer than the longest token
#              lifetime), the verifiers stop trusting the old key.
#
# If rotate stops before it is complete, run it again: it continues from
# the step where it stopped. A dev token has no overlap: rotate replaces it
# and restarts the service that accepts it.
#
# The dev tokens are random strings for the static-dev provider. Only
# images built with -tags authdev (the Tilt images) accept them. The
# production images refuse the static-dev provider at startup.
#
# Requirements: openssl, kubectl.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KEYS_DIR="${AUTH_KEYS_DIR:-${REPO_ROOT}/.dev/auth}"
SECRET="dynamos-auth"
# The longest token lifetime is 5m (job tokens), plus 30s clock leeway.
GRACE="${AUTH_ROTATION_GRACE:-600}"
ROLLOUT_TIMEOUT="180s"

SIGNING_KEYS=(orchestrator transfer-process-service)
DEV_TOKENS=(dev-user-token dev-operator-token)

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

kc() {
    if [[ -n "${KUBE_CONTEXT:-}" ]]; then
        kubectl --context "${KUBE_CONTEXT}" "$@"
    else
        kubectl "$@"
    fi
}

# ---------------------------------------------------------------------------
# Who uses which key. Workloads are namespace/deployment.
# ---------------------------------------------------------------------------
issuers_of() {
    case "$1" in
        orchestrator) echo "orchestrator/orchestrator" ;;
        transfer-process-service)
            echo "transfer-process-service/transfer-process-service-uva" \
                 "transfer-process-service/transfer-process-service-vu" \
                 "transfer-process-service/transfer-process-service-surf" ;;
    esac
}

verifiers_of() {
    case "$1" in
        orchestrator) echo "uva/uva vu/vu surf/surf" ;;
        transfer-process-service) echo "api-gateway/api-gateway" ;;
        dev-user-token) echo "api-gateway/api-gateway" ;;
        dev-operator-token) echo "orchestrator/orchestrator" ;;
    esac
}

is_signing_key() { [[ " ${SIGNING_KEYS[*]} " == *" $1 "* ]]; }
is_dev_token() { [[ " ${DEV_TOKENS[*]} " == *" $1 "* ]]; }

# ---------------------------------------------------------------------------
# Files. For a signing key NAME:
#   NAME.key.pem        current private key (the issuer signs with it)
#   NAME.next.key.pem   rotation step 1 done, step 2 not done
#   NAME.old.pub.pem    old public key, still trusted until "retire"
#   NAME.rotated-at     time of step 2 (seconds since epoch)
# NAME.pub.pem is made again from NAME.key.pem on each run.
# ---------------------------------------------------------------------------
f() { echo "${KEYS_DIR}/$1"; }

new_key() {  # path
    (umask 077 && openssl genpkey -algorithm ed25519 -out "$1")
}

pub_of() {  # private key path
    openssl pkey -in "$1" -pubout
}

make_key() {  # name
    if [[ ! -f "$(f "$1.key.pem")" ]]; then
        echo "  Make key $1"
        new_key "$(f "$1.key.pem")"
    fi
    pub_of "$(f "$1.key.pem")" >"$(f "$1.pub.pem")"
}

make_token() {  # name
    if [[ ! -f "$(f "$1")" ]]; then
        echo "  Make $1"
        new_token "$1"
    fi
}

new_token() {  # name
    (umask 077 && openssl rand -hex 32 | tr -d '\n' >"$(f "$1.tmp")")
    mv "$(f "$1.tmp")" "$(f "$1")"
}

# trusted NAME: all public keys the verifiers of NAME must accept now.
trusted() {
    cat "$(f "$1.pub.pem")"
    if [[ -f "$(f "$1.next.key.pem")" ]]; then
        pub_of "$(f "$1.next.key.pem")"
    fi
    if [[ -f "$(f "$1.old.pub.pem")" ]]; then
        cat "$(f "$1.old.pub.pem")"
    fi
}

# ---------------------------------------------------------------------------
# Secrets
# ---------------------------------------------------------------------------
# Bundles of public keys, made again on each apply_all.
STAGE="$(mktemp -d)"
# shellcheck disable=SC2064
trap "rm -rf '${STAGE}'" EXIT

apply_secret() {  # namespace entry...   entry: key=path
    local ns="$1" args=() names=()
    shift
    for e in "$@"; do
        args+=("--from-file=${e}")
        names+=("${e%%=*}")
    done
    kc get namespace "${ns}" >/dev/null 2>&1 || die "Namespace ${ns} does not exist. Install the namespaces chart first."
    # Server-side apply: kubectl does not copy the data into the
    # last-applied-configuration annotation.
    kc create secret generic "${SECRET}" -n "${ns}" "${args[@]}" --dry-run=client -o yaml |
        kc apply --server-side --force-conflicts -f - >/dev/null
    echo "  Secret ${ns}/${SECRET}: ${names[*]}"
}

apply_all() {
    for k in "${SIGNING_KEYS[@]}"; do
        trusted "${k}" >"${STAGE}/${k}.pub.pem"
    done
    apply_secret orchestrator \
        "orchestrator.key.pem=$(f orchestrator.key.pem)" \
        "dev-operator-token=$(f dev-operator-token)"
    for ns in uva vu surf; do
        apply_secret "${ns}" "orchestrator.pub.pem=${STAGE}/orchestrator.pub.pem"
    done
    apply_secret transfer-process-service \
        "transfer-process-service.key.pem=$(f transfer-process-service.key.pem)"
    apply_secret api-gateway \
        "transfer-process-service.pub.pem=${STAGE}/transfer-process-service.pub.pem" \
        "dev-user-token=$(f dev-user-token)"
}

ensure_all() {
    for tool in openssl kubectl; do
        command -v "${tool}" >/dev/null || die "'${tool}' is not installed."
    done
    echo "Auth keys in ${KEYS_DIR}"
    mkdir -p "${KEYS_DIR}"
    chmod 700 "${KEYS_DIR}"
    for k in "${SIGNING_KEYS[@]}"; do make_key "${k}"; done
    for t in "${DEV_TOKENS[@]}"; do make_token "${t}"; done
}

# restart workload...: restart the Deployments that exist and wait for them.
# A workload that is not deployed (for example "tilt up -- --only ...")
# reads the Secret when it starts.
restart() {
    local w ns name started=()
    for w in "$@"; do
        ns="${w%%/*}" name="${w#*/}"
        if kc get deployment "${name}" -n "${ns}" >/dev/null 2>&1; then
            kc rollout restart deployment "${name}" -n "${ns}" >/dev/null
            started+=("${w}")
        else
            echo "  ${w} is not deployed, skip"
        fi
    done
    for w in ${started[@]+"${started[@]}"}; do
        ns="${w%%/*}" name="${w#*/}"
        echo "  Wait for ${w}"
        kc rollout status deployment "${name}" -n "${ns}" --timeout="${ROLLOUT_TIMEOUT}" >/dev/null ||
            die "${w} did not become ready. Fix it, then run this command again."
    done
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_apply() {
    ensure_all
    apply_all
}

cmd_print() {
    for t in "${DEV_TOKENS[@]}"; do
        [[ -f "$(f "${t}")" ]] || die "$(f "${t}") does not exist. Run $0 first."
    done
    printf 'export DYNAMOS_USER_TOKEN=%q\n' "$(cat "$(f dev-user-token)")"
    printf 'export DYNAMOS_OPERATOR_TOKEN=%q\n' "$(cat "$(f dev-operator-token)")"
}

cmd_rotate() {
    local name="${1:-}"
    ensure_all

    if is_dev_token "${name}"; then
        echo "Rotate ${name}"
        new_token "${name}"
        apply_all
        # shellcheck disable=SC2046
        restart $(verifiers_of "${name}")
        echo "Done. Old ${name} is no longer accepted. Show the new one: $0 --print"
        return
    fi
    is_signing_key "${name}" || die "Unknown key '${name}'. Use one of: ${SIGNING_KEYS[*]} ${DEV_TOKENS[*]}"

    if [[ -f "$(f "${name}.rotated-at")" ]]; then
        die "The previous rotation of ${name} is not retired. Run: $0 retire ${name}"
    fi

    echo "Rotate ${name}"
    # Step 1 is not complete while the next key exists. Without an old
    # public key, the rotation has not started.
    if [[ -f "$(f "${name}.next.key.pem")" || ! -f "$(f "${name}.old.pub.pem")" ]]; then
        # Step 1: the verifiers trust the current and the next key.
        if [[ ! -f "$(f "${name}.next.key.pem")" ]]; then
            echo "  Make next key"
            new_key "$(f "${name}.next.key.pem")"
        fi
        apply_all
        # shellcheck disable=SC2046
        restart $(verifiers_of "${name}")

        # Step 2: the next key becomes the current key. Keep the old public
        # key first, so a stop between the two moves loses nothing.
        cp "$(f "${name}.pub.pem")" "$(f "${name}.old.pub.pem")"
        mv "$(f "${name}.next.key.pem")" "$(f "${name}.key.pem")"
        pub_of "$(f "${name}.key.pem")" >"$(f "${name}.pub.pem")"
    fi
    apply_all
    # shellcheck disable=SC2046
    restart $(issuers_of "${name}")
    date +%s >"$(f "${name}.rotated-at")"
    echo "Done. ${name} signs with the new key. The old key is still trusted."
    echo "After $((GRACE / 60)) min, run: $0 retire ${name}"
}

cmd_retire() {
    local name="${1:-}" force="${2:-}"
    is_signing_key "${name}" || die "Unknown signing key '${name}'. Use one of: ${SIGNING_KEYS[*]}"
    ensure_all
    [[ -f "$(f "${name}.old.pub.pem")" ]] || die "${name} has no old key to retire."
    [[ -f "$(f "${name}.rotated-at")" ]] || die "The rotation of ${name} is not complete. Run: $0 rotate ${name}"

    local wait=$(( $(cat "$(f "${name}.rotated-at")") + GRACE - $(date +%s) ))
    if (( wait > 0 )) && [[ "${force}" != "--force" ]]; then
        die "Tokens that the old key signed can still be valid. Wait ${wait}s, or use: $0 retire ${name} --force"
    fi

    echo "Retire the old key of ${name}"
    rm -f "$(f "${name}.old.pub.pem")"
    apply_all
    # shellcheck disable=SC2046
    restart $(verifiers_of "${name}")
    rm -f "$(f "${name}.rotated-at")"
    echo "Done. Only the current key of ${name} is trusted."
}

cmd_status() {
    local k state
    echo "Auth keys in ${KEYS_DIR}"
    for k in "${SIGNING_KEYS[@]}"; do
        if [[ ! -f "$(f "${k}.key.pem")" ]]; then
            state="missing (run $0)"
        elif [[ -f "$(f "${k}.next.key.pem")" ]]; then
            state="rotation in progress (run: $0 rotate ${k})"
        elif [[ -f "$(f "${k}.rotated-at")" ]]; then
            state="rotated at $(date -d "@$(cat "$(f "${k}.rotated-at")")" '+%F %T' 2>/dev/null || cat "$(f "${k}.rotated-at")"), old key still trusted (then: $0 retire ${k})"
        elif [[ -f "$(f "${k}.old.pub.pem")" ]]; then
            state="rotation in progress (run: $0 rotate ${k})"
        else
            state="ok"
        fi
        printf '  %-26s %s\n' "${k}" "${state}"
    done
    for k in "${DEV_TOKENS[@]}"; do
        [[ -f "$(f "${k}")" ]] && state="ok" || state="missing (run $0)"
        printf '  %-26s %s\n' "${k}" "${state}"
    done
}

case "${1:-}" in
    "") cmd_apply ;;
    --print) cmd_print ;;
    rotate) shift; cmd_rotate "$@" ;;
    retire) shift; cmd_retire "$@" ;;
    status) cmd_status ;;
    *) sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
