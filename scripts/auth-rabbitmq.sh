# shellcheck shell=bash
# RabbitMQ users for DYNAMOS. Sourced by scripts/auth-secrets.sh; do not
# run it alone. It uses f, kc, die and apply_secret from that script.
#
# Every sidecar has its own RabbitMQ user. Before this, all services shared
# "normal_user" (full rights on everything) and "guest" was an
# administrator with a known password that worked from any pod.
#
#   user              sidecar of           may consume       may publish to
#   orchestrator      orchestrator         orchestrator-in   policyEnforcer-in, api-gateway-in, <STEWARD>-in
#   policy-enforcer   policy-enforcer      policyEnforcer-in orchestrator-in
#   api-gateway       api-gateway          api-gateway-in    policyEnforcer-in
#   agent-<s>         agent <S>            <S>-in, <s> jobs  <STEWARD>-in (third party), <s> job queues
#   job-<s>           jobs of agent <S>    <s> job queues    <S>-in
#   dynamos-admin     (management UI)      everything        everything
#
# <s> is the steward in lower case, <S> in upper case. A job queue name
# ends with the steward and a counter (buildLocalJobName in
# go/cmd/agent/composition_request_handler.go), for example
# "jorrit-stutterheim-1a2b3c4duva1". The permissions depend on that.
#
# The services can not configure topic_exchange (that also allows a
# delete); the definitions create it and the sidecars check it passively.
#
# The sidecars also put their user in the "user_id" of each message. The
# broker refuses a wrong user_id, and the receivers check which user may
# send which message type (go/cmd/sidecar/sender_auth.go).
#
# Files: <keys dir>/rabbitmq/<user>.password. Secret "dynamos-rabbitmq":
#
#   namespace      keys
#   orchestrator   orchestrator, policy-enforcer
#   api-gateway    api-gateway
#   uva, vu, surf  agent-<s>, job-<s>

RMQ_SECRET="dynamos-rabbitmq"
RMQ_STEWARDS=(uva vu surf)
RMQ_ADMIN="dynamos-admin"
# Users from the old setup. They are removed from the broker.
RMQ_LEGACY_USERS=(normal_user guest)

rmq_users() {
    local s
    echo orchestrator policy-enforcer api-gateway
    for s in "${RMQ_STEWARDS[@]}"; do echo "agent-${s}" "job-${s}"; done
}

rmq_pw_file() { f "rabbitmq/$1.password"; }

# rmq_perms USER: prints "configure write read topic-write topic-read".
# Every user may bind its queues with their own name as the routing key,
# and needs write on dead-letter-exchange to declare a queue with it.
rmq_perms() {
    local u="$1" q tw s S
    case "${u}" in
        orchestrator) q='orchestrator-in' tw='policyEnforcer-in|api-gateway-in|[A-Z0-9]+-in' ;;
        policy-enforcer) q='policyEnforcer-in' tw='orchestrator-in' ;;
        api-gateway) q='api-gateway-in' tw='policyEnforcer-in' ;;
        agent-*) s="${u#agent-}" S="$(tr '[:lower:]' '[:upper:]' <<<"${s}")"
            q="${S}-in|.*${s}[0-9]+" tw="[A-Z0-9]+-in|.*${s}[0-9]+" ;;
        job-*) s="${u#job-}" S="$(tr '[:lower:]' '[:upper:]' <<<"${s}")"
            q=".*${s}[0-9]+" tw="${S}-in" ;;
        "${RMQ_ADMIN}") echo '.* .* .* .* .*'; return ;;
        *) die "No RabbitMQ permissions for user ${u}" ;;
    esac
    echo "^(${q})\$ ^(${q}|topic_exchange|dead-letter-exchange)\$ ^(${q}|topic_exchange)\$ ^(${tw})\$ ^(${q})\$"
}

# rabbit_hash PASSWORD: the RabbitMQ SHA-256 password hash,
# base64(salt + sha256(salt + password)) with a 4-byte salt.
rabbit_hash() {
    local t
    t="$(mktemp -d)"
    openssl rand -out "${t}/salt" 4
    { cat "${t}/salt"; printf '%s' "$1"; } | openssl dgst -sha256 -binary >"${t}/hash"
    cat "${t}/salt" "${t}/hash" | openssl base64 -A
    rm -rf "${t}"
}

rmq_new_password() {  # user
    local file
    file="$(rmq_pw_file "$1")"
    (umask 077 && openssl rand -hex 24 | tr -d '\n' >"${file}.tmp")
    mv "${file}.tmp" "${file}"
}

rmq_ensure_passwords() {
    local u
    mkdir -p "$(f rabbitmq)"
    chmod 700 "$(f rabbitmq)"
    for u in $(rmq_users) "${RMQ_ADMIN}"; do
        if [[ ! -f "$(rmq_pw_file "${u}")" ]]; then
            echo "  Make RabbitMQ password ${u}"
            rmq_new_password "${u}"
        fi
    done
}

rmq_apply_secrets() {
    local s
    apply_secret_named "${RMQ_SECRET}" orchestrator \
        "orchestrator=$(rmq_pw_file orchestrator)" \
        "policy-enforcer=$(rmq_pw_file policy-enforcer)"
    apply_secret_named "${RMQ_SECRET}" api-gateway "api-gateway=$(rmq_pw_file api-gateway)"
    for s in "${RMQ_STEWARDS[@]}"; do
        apply_secret_named "${RMQ_SECRET}" "${s}" \
            "agent-${s}=$(rmq_pw_file "agent-${s}")" \
            "job-${s}=$(rmq_pw_file "job-${s}")"
    done
}

# rmq_definitions: users, permissions and topic permissions as RabbitMQ
# definitions JSON. Passwords are hex and the patterns have no quote or
# backslash, so no JSON escaping is needed.
rmq_definitions() {
    local u sep="" users="" perms="" topics="" c w r tw tr tags
    for u in $(rmq_users) "${RMQ_ADMIN}"; do
        read -r c w r tw tr <<<"$(rmq_perms "${u}")"
        tags='[]'
        [[ "${u}" == "${RMQ_ADMIN}" ]] && tags='["administrator"]'
        users+="${sep}{\"name\":\"${u}\",\"password_hash\":\"$(rabbit_hash "$(cat "$(rmq_pw_file "${u}")")")\",\"hashing_algorithm\":\"rabbit_password_hashing_sha256\",\"tags\":${tags}}"
        perms+="${sep}{\"user\":\"${u}\",\"vhost\":\"/\",\"configure\":\"${c}\",\"write\":\"${w}\",\"read\":\"${r}\"}"
        topics+="${sep}{\"user\":\"${u}\",\"vhost\":\"/\",\"exchange\":\"topic_exchange\",\"write\":\"${tw}\",\"read\":\"${tr}\"}"
        sep=","
    done
    printf '{"users":[%s],"permissions":[%s],"topic_permissions":[%s]}\n' "${users}" "${perms}" "${topics}"
}

rmq_exec() { kc exec -i -n core deploy/rabbitmq -c rabbitmq -- "$@"; }

# rmq_apply_broker: create or update the users on the running broker,
# remove the old shared users, and write the result to the definitions
# file that RabbitMQ loads at boot (on its PVC), so a restart does not add
# the old users again.
rmq_apply_broker() {
    local last expect i u
    kc get deployment rabbitmq -n core >/dev/null 2>&1 ||
        die "RabbitMQ (deployment core/rabbitmq) is not installed. Install the core chart first."
    kc rollout status deployment rabbitmq -n core --timeout=120s >/dev/null ||
        die "RabbitMQ is not ready."
    # A ready pod can still have the "rabbit" app mid-boot, most often
    # just after a cold start of the kind node.
    for i in $(seq 1 20); do
        rmq_exec rabbitmqctl -q await_startup --timeout 10 >/dev/null 2>&1 && break
        [[ "${i}" -eq 20 ]] && die "The RabbitMQ app did not start."
        sleep 3
    done

    rmq_definitions | rmq_exec sh -c 'umask 077 && cat >/tmp/dynamos-users.json'
    rmq_exec rabbitmqctl -q import_definitions /tmp/dynamos-users.json >/dev/null

    # The import is asynchronous: users first, topic permissions last.
    # Wait until the last user has its topic permissions and its current
    # password (after a rotation the permissions are already there).
    last="${RMQ_ADMIN}"
    read -r _ _ _ expect _ <<<"$(rmq_perms "${last}")"
    for i in $(seq 1 60); do
        if rmq_exec rabbitmqctl -q list_topic_permissions 2>/dev/null |
            awk -v u="${last}" -v w="${expect}" '$1 == u && $3 == w { found = 1 } END { exit !found }' &&
            rmq_exec rabbitmqctl -q authenticate_user "${last}" "$(cat "$(rmq_pw_file "${last}")")" >/dev/null 2>&1; then
            break
        fi
        [[ "${i}" -eq 60 ]] && die "RabbitMQ did not finish the import of the users."
        sleep 2
    done
    rmq_exec rm -f /tmp/dynamos-users.json

    for u in "${RMQ_LEGACY_USERS[@]}"; do
        if rmq_exec rabbitmqctl -q list_users 2>/dev/null | awk -v u="${u}" '$1 == u { found = 1 } END { exit !found }'; then
            rmq_exec rabbitmqctl -q delete_user "${u}" >/dev/null
            echo "  RabbitMQ: removed old user ${u}"
        fi
    done

    if rmq_exec test -f /mnt/definitions.json; then
        rmq_exec rabbitmqctl -q export_definitions /mnt/definitions.json >/dev/null
    fi
    echo "  RabbitMQ: users $(rmq_users | tr '\n' ' ')${RMQ_ADMIN}"
}

# Workloads with a RabbitMQ sidecar. Job pods read the Secret when the
# agent starts them.
rmq_workloads() {
    local s
    echo orchestrator/orchestrator orchestrator/policy-enforcer api-gateway/api-gateway
    for s in "${RMQ_STEWARDS[@]}"; do echo "${s}/${s}"; done
}

# rmq_rotate: new passwords for every user. RabbitMQ has one password per
# user, so there is no overlap: open connections stay up, and the restart
# makes the sidecars connect with the new password.
rmq_rotate() {
    local u
    echo "Rotate the RabbitMQ passwords"
    for u in $(rmq_users) "${RMQ_ADMIN}"; do rmq_new_password "${u}"; done
    rmq_apply_secrets
    rmq_apply_broker
    # shellcheck disable=SC2046
    restart $(rmq_workloads)
    echo "Done. Running jobs keep their connection; new jobs use the new passwords."
}
