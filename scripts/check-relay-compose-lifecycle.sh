#!/usr/bin/env bash
# Runs the shipped relay Compose baseline against isolated synthetic material.
# The script never enables shell tracing and never writes fixture contents to
# stdout because CI logs are retained outside the protected fixture directory.
set -Eeuo pipefail

umask 077

readonly ACTION="${1:-}"
readonly RUN_TOKEN="${2:-}"
readonly DEPLOYMENT_ANCHOR="/etc/rig-relay"
readonly ENVIRONMENT_FILE="${DEPLOYMENT_ANCHOR}/relay.env"
readonly SECRET_DIRECTORY="${DEPLOYMENT_ANCHOR}/secrets"
readonly STATE_DIRECTORY="/tmp/rig-relay-lifecycle-${RUN_TOKEN}"
readonly STATE_MARKER="${STATE_DIRECTORY}/fixture-owner"
readonly ANCHOR_MARKER="${DEPLOYMENT_ANCHOR}/.rig-relay-lifecycle-owner"
readonly COMPOSE_PROJECT="rigrelayci${RUN_TOKEN//-/}"
readonly EDGE_NETWORK="rig-relay-edge-${RUN_TOKEN}"
readonly REGISTRY_CONTAINER="rig-relay-registry-${RUN_TOKEN}"
readonly REGISTRY_IMAGE="docker.io/library/registry:2.8.3@sha256:46faa9a1ae6813194b53921a370f2f4f8c5e1aae228a89bceafef5847a6a3278"
readonly RELAY_TAG="127.0.0.1:5000/rig-relay:ci-${RUN_TOKEN}"

SCRIPT_DIRECTORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly SCRIPT_DIRECTORY
REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIRECTORY}/.." && pwd -P)"
readonly REPOSITORY_ROOT
readonly COMPOSE_FILE="${REPOSITORY_ROOT}/deploy/relay/compose.yaml"
readonly MIGRATION_DIRECTORY="${REPOSITORY_ROOT}/internal/relay/store/migrations"
CURRENT_PHASE="initialization"

fail() {
  printf 'relay Compose lifecycle check failed: %s\n' "$1" >&2
  exit 1
}

on_error() {
  local status=$?
  trap - ERR
  printf 'relay Compose lifecycle command failed: phase=%s status=%d\n' "${CURRENT_PHASE}" "${status}" >&2
  return "${status}"
}

valid_scope() {
  [[ "${RUN_TOKEN}" =~ ^[0-9]+-[0-9]+$ ]] || fail "invalid run token"
  [[ "${STATE_DIRECTORY}" == "/tmp/rig-relay-lifecycle-${RUN_TOKEN}" ]] || fail "invalid state scope"
  [[ "${DEPLOYMENT_ANCHOR}" == "/etc/rig-relay" ]] || fail "invalid deployment scope"
  [[ "${COMPOSE_PROJECT}" =~ ^[a-z0-9]{1,63}$ ]] || fail "invalid Compose project"
  [[ "${EDGE_NETWORK}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$ ]] || fail "invalid edge network"
}

require_root() {
  [[ "$(id -u)" == "0" ]] || fail "root is required for protected fixtures"
}

compose() {
  docker compose \
    --project-name "${COMPOSE_PROJECT}" \
    --env-file "${ENVIRONMENT_FILE}" \
    --file "${COMPOSE_FILE}" \
    "$@"
}

prepare_required_log() {
  local destination="$1"
  [[ "${destination}" == "${STATE_DIRECTORY}/"*".log" ]] || return 1
  : >"${destination}.required-log"
  chmod 0600 "${destination}.required-log"
}

capture_logs() {
  local destination="$1"
  [[ "${destination}" == "${STATE_DIRECTORY}/"*".log" ]] || return 1
  [[ -f "${STATE_DIRECTORY}/compose-attempted" ]] || return 0
  [[ -r "${ENVIRONMENT_FILE}" ]] || return 1
  prepare_required_log "${destination}" || return 1
  if ! compose logs --no-color --no-log-prefix >"${destination}" 2>&1; then
    chmod 0600 "${destination}" 2>/dev/null || true
    return 1
  fi
  chmod 0600 "${destination}" || return 1
  [[ -f "${destination}" && -r "${destination}" ]]
}

scan_captured_logs() {
  local pattern_file="${STATE_DIRECTORY}/secret-patterns"
  local pattern_count
  local log_file
  local marker
  local grep_status
  local log_list="${STATE_DIRECTORY}/log-files.list"
  local marker_list="${STATE_DIRECTORY}/required-log-markers.list"

  if [[ ! -f "${STATE_DIRECTORY}/patterns-ready" ]]; then
    if [[ -f "${STATE_DIRECTORY}/compose-attempted" ]]; then
      printf 'relay lifecycle secret scan failed: pattern readiness missing\n' >&2
      return 1
    fi
    return 0
  fi
  if [[ ! -s "${pattern_file}" ]]; then
    printf 'relay lifecycle secret scan failed: pattern set missing\n' >&2
    return 1
  fi
  pattern_count="$(awk 'NF { count++ } END { print count + 0 }' "${pattern_file}")"
  if [[ ! "${pattern_count}" =~ ^[0-9]+$ ]] || (( pattern_count < 8 )); then
    printf 'relay lifecycle secret scan failed: invalid pattern set\n' >&2
    return 1
  fi
  if ! find "${STATE_DIRECTORY}" -maxdepth 1 -type f -name '*.required-log' -print0 >"${marker_list}"; then
    printf 'relay lifecycle secret scan failed: required-log discovery failed\n' >&2
    return 1
  fi
  while IFS= read -r -d '' marker; do
    log_file="${marker%.required-log}"
    if [[ ! -f "${log_file}" || ! -r "${log_file}" ]]; then
      printf 'relay lifecycle secret scan failed: required log unavailable\n' >&2
      return 1
    fi
  done <"${marker_list}"
  if ! find "${STATE_DIRECTORY}" -maxdepth 1 -type f -name '*.log' -print0 >"${log_list}"; then
    printf 'relay lifecycle secret scan failed: log discovery failed\n' >&2
    return 1
  fi
  while IFS= read -r -d '' log_file; do
    if grep --binary-files=text --fixed-strings --file="${pattern_file}" --quiet -- "${log_file}"; then
      printf 'relay lifecycle secret scan failed: protected material detected\n' >&2
      return 1
    else
      grep_status=$?
      if (( grep_status != 1 )); then
        printf 'relay lifecycle secret scan failed: log search failed\n' >&2
        return 1
      fi
    fi
  done <"${log_list}"
  return 0
}

runtime_scope_is_empty() {
  local resources
  resources="$(docker ps -a --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" --format '{{.ID}}')" || return 2
  [[ -z "${resources}" ]] || return 1
  resources="$(docker volume ls --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" --format '{{.Name}}')" || return 2
  [[ -z "${resources}" ]] || return 1
  resources="$(docker network ls --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" --format '{{.ID}}')" || return 2
  [[ -z "${resources}" ]] || return 1
}

assert_runtime_scope_empty() {
  runtime_scope_is_empty || fail "Compose project scope already exists"
  docker container inspect "${REGISTRY_CONTAINER}" >/dev/null 2>&1 && fail "registry container scope already exists"
  docker network inspect "${EDGE_NETWORK}" >/dev/null 2>&1 && fail "edge network scope already exists"
  return 0
}

remove_owned_tree() {
  local path="$1"
  [[ ! -e "${path}" ]] && return 0
  [[ -d "${path}" && ! -L "${path}" ]] || return 1
  find "${path}" -xdev -depth -delete
}

cleanup() {
  local original_status="${1:-0}"
  local cleanup_status=0
  local leak_status=0
  local current_network_id=""
  local recorded_network_id=""
  local current_registry_id=""
  local recorded_registry_id=""
  local registry_owner=""
  local current_anchor_identity=""
  local recorded_anchor_identity=""
  local scope_present=0

  trap - EXIT INT TERM HUP ERR
  set +e

  if [[ -e "${STATE_DIRECTORY}" || -L "${STATE_DIRECTORY}" || -e "${DEPLOYMENT_ANCHOR}" || -L "${DEPLOYMENT_ANCHOR}" ]]; then
    scope_present=1
  fi
  if (( scope_present == 0 )); then
    if ! runtime_scope_is_empty ||
       docker container inspect "${REGISTRY_CONTAINER}" >/dev/null 2>&1 ||
       docker network inspect "${EDGE_NETWORK}" >/dev/null 2>&1; then
      printf 'relay lifecycle cleanup failed: unowned runtime scope remains\n' >&2
      cleanup_status=1
    fi
  fi
  if (( scope_present == 1 )) &&
     { [[ ! -d "${STATE_DIRECTORY}" || -L "${STATE_DIRECTORY}" || ! -f "${STATE_MARKER}" ]] ||
       [[ "$(<"${STATE_MARKER}")" != "${RUN_TOKEN}" ]]; }; then
    printf 'relay lifecycle cleanup failed: fixture ownership unavailable\n' >&2
    cleanup_status=1
  elif (( scope_present == 1 )); then
    capture_logs "${STATE_DIRECTORY}/cleanup.log" || cleanup_status=1
    scan_captured_logs || leak_status=1

    if [[ -r "${ENVIRONMENT_FILE}" ]]; then
      compose down --volumes --remove-orphans --timeout 30 >/dev/null 2>&1 || cleanup_status=1
    fi
    if ! runtime_scope_is_empty; then
      printf 'relay lifecycle cleanup failed: Compose project resources remain\n' >&2
      cleanup_status=1
    fi

    if [[ -s "${STATE_DIRECTORY}/edge-network-id" ]]; then
      recorded_network_id="$(<"${STATE_DIRECTORY}/edge-network-id")"
      current_network_id="$(docker network inspect --format '{{.Id}}' "${EDGE_NETWORK}" 2>/dev/null)"
      if [[ -z "${current_network_id}" ]]; then
        printf 'relay lifecycle cleanup failed: recorded edge network disappeared\n' >&2
        cleanup_status=1
      else
        if [[ "${current_network_id}" == "${recorded_network_id}" ]]; then
          docker network rm "${EDGE_NETWORK}" >/dev/null 2>&1 || cleanup_status=1
        else
          printf 'relay lifecycle cleanup failed: edge network identity changed\n' >&2
          cleanup_status=1
        fi
      fi
      if docker network inspect "${EDGE_NETWORK}" >/dev/null 2>&1; then
        printf 'relay lifecycle cleanup failed: edge network remains\n' >&2
        cleanup_status=1
      fi
    elif docker network inspect "${EDGE_NETWORK}" >/dev/null 2>&1; then
      printf 'relay lifecycle cleanup failed: unowned edge network remains\n' >&2
      cleanup_status=1
    fi

    if [[ -s "${STATE_DIRECTORY}/registry-container-id" ]]; then
      recorded_registry_id="$(<"${STATE_DIRECTORY}/registry-container-id")"
      current_registry_id="$(docker container inspect --format '{{.Id}}' "${REGISTRY_CONTAINER}" 2>/dev/null)"
      registry_owner="$(docker container inspect --format '{{index .Config.Labels "rig.relay.lifecycle.run"}}' "${REGISTRY_CONTAINER}" 2>/dev/null)"
      if [[ -z "${current_registry_id}" ]]; then
        printf 'relay lifecycle cleanup failed: recorded registry container disappeared\n' >&2
        cleanup_status=1
      else
        if [[ "${current_registry_id}" == "${recorded_registry_id}" && "${registry_owner}" == "${RUN_TOKEN}" ]]; then
          docker rm --force "${recorded_registry_id}" >/dev/null 2>&1 || cleanup_status=1
        else
          printf 'relay lifecycle cleanup failed: registry identity changed\n' >&2
          cleanup_status=1
        fi
      fi
      if docker container inspect "${REGISTRY_CONTAINER}" >/dev/null 2>&1 ||
         docker container inspect "${recorded_registry_id}" >/dev/null 2>&1; then
        printf 'relay lifecycle cleanup failed: registry container remains\n' >&2
        cleanup_status=1
      fi
    elif docker container inspect "${REGISTRY_CONTAINER}" >/dev/null 2>&1; then
      printf 'relay lifecycle cleanup failed: unowned registry container remains\n' >&2
      cleanup_status=1
    fi

    if [[ -s "${STATE_DIRECTORY}/anchor-identity" ]]; then
      recorded_anchor_identity="$(<"${STATE_DIRECTORY}/anchor-identity")"
      current_anchor_identity="$(stat --format '%d:%i:%u:%g:%a:%F' "${DEPLOYMENT_ANCHOR}" 2>/dev/null)"
      if [[ "${current_anchor_identity}" == "${recorded_anchor_identity}" && -f "${ANCHOR_MARKER}" &&
            "$(stat --format '%u:%g:%a:%F' "${ANCHOR_MARKER}" 2>/dev/null)" == "0:0:600:regular file" &&
            "$(<"${ANCHOR_MARKER}")" == "${RUN_TOKEN}" ]]; then
        remove_owned_tree "${DEPLOYMENT_ANCHOR}" || cleanup_status=1
      else
        printf 'relay lifecycle cleanup failed: deployment anchor identity changed\n' >&2
        cleanup_status=1
      fi
      if [[ -e "${DEPLOYMENT_ANCHOR}" || -L "${DEPLOYMENT_ANCHOR}" ]]; then
        printf 'relay lifecycle cleanup failed: deployment anchor remains\n' >&2
        cleanup_status=1
      fi
    elif [[ -e "${DEPLOYMENT_ANCHOR}" || -L "${DEPLOYMENT_ANCHOR}" ]]; then
      printf 'relay lifecycle cleanup failed: unowned deployment anchor remains\n' >&2
      cleanup_status=1
    fi
    remove_owned_tree "${STATE_DIRECTORY}" || cleanup_status=1
    if [[ -e "${STATE_DIRECTORY}" || -L "${STATE_DIRECTORY}" ]]; then
      printf 'relay lifecycle cleanup failed: state directory remains\n' >&2
      cleanup_status=1
    fi
  fi

  if (( leak_status != 0 )); then
    return 70
  fi
  if (( cleanup_status != 0 )); then
    return 71
  fi
  if (( original_status != 0 )); then
    return "${original_status}"
  fi
  return 0
}

on_exit() {
  local status=$?
  local result=0
  cleanup "${status}" || result=$?
  exit "${result}"
}

require_commands() {
  local command
  local compose_version
  for command in docker openssl pwsh stat id curl awk grep find base64 od sha256sum \
    install sed sort head tail seq tr cut cmp diff date ss uname sleep chmod chown cat jq; do
    command -v "${command}" >/dev/null 2>&1 || fail "required command unavailable"
  done
  docker info >/dev/null 2>&1 || fail "Docker daemon unavailable"
  compose_version="$(docker compose version --short 2>/dev/null | sed 's/^v//')"
  [[ "${compose_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] || fail "Docker Compose version unavailable"
  [[ "$(printf '%s\n%s\n' '2.30.0' "${compose_version}" | sort -V | head -n 1)" == "2.30.0" ]] ||
    fail "Docker Compose 2.30.0 or newer is required"
}

wait_for_registry() {
  local attempt
  for attempt in $(seq 1 30); do
    if curl --fail --silent --show-error --max-time 2 http://127.0.0.1:5000/v2/ >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "isolated registry did not become ready"
}

create_registry() {
  local listeners
  if ! listeners="$(ss -H -ltn 'sport = :5000' 2>/dev/null)"; then
    fail "localhost listener inspection failed"
  fi
  if [[ -n "${listeners}" ]]; then
    fail "localhost registry port is already in use"
  fi
  if docker container inspect "${REGISTRY_CONTAINER}" >/dev/null 2>&1; then
    fail "registry container scope already exists"
  fi
  prepare_required_log "${STATE_DIRECTORY}/registry-image-pull.log"
  docker pull "${REGISTRY_IMAGE}" >"${STATE_DIRECTORY}/registry-image-pull.log" 2>&1
  docker run --detach \
    --name "${REGISTRY_CONTAINER}" \
    --cidfile "${STATE_DIRECTORY}/registry-container-id" \
    --label "rig.relay.lifecycle.run=${RUN_TOKEN}" \
    --publish 127.0.0.1:5000:5000/tcp \
    --read-only \
    --tmpfs /var/lib/registry:rw,noexec,nosuid,nodev,size=512m \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    --pids-limit 128 \
    --memory 512m \
    --cpus 1 \
    "${REGISTRY_IMAGE}" >/dev/null
  chmod 0600 "${STATE_DIRECTORY}/registry-container-id"
  [[ "$(<"${STATE_DIRECTORY}/registry-container-id")" =~ ^[0-9a-f]{64}$ ]] ||
    fail "registry container identity unavailable"
  [[ "$(docker container inspect --format '{{index .Config.Labels "rig.relay.lifecycle.run"}}' "${REGISTRY_CONTAINER}")" == "${RUN_TOKEN}" ]] ||
    fail "registry container ownership unavailable"
  wait_for_registry
  scan_captured_logs
}

create_fixture() {
  local postgres_password
  local github_client_secret
  local github_webhook_secret
  local relay_postgres_dsn
  local tls_name="relay-${RUN_TOKEN}.ci.invalid"
  local work="${STATE_DIRECTORY}/certificate-work"
  local enrollment_hex
  local enrollment_base64
  local key_file
  local extracted_patterns
  local payload_count
  local private_key_patterns="${STATE_DIRECTORY}/private-key-patterns"

  [[ ! -e "${STATE_DIRECTORY}" && ! -L "${STATE_DIRECTORY}" ]] || fail "state scope already exists"
  [[ ! -e "${DEPLOYMENT_ANCHOR}" && ! -L "${DEPLOYMENT_ANCHOR}" ]] || fail "deployment anchor already exists"
  install -d -o root -g root -m 0700 "${STATE_DIRECTORY}"
  printf '%s' "${RUN_TOKEN}" >"${STATE_MARKER}"
  chmod 0600 "${STATE_MARKER}"

  install -d -o root -g root -m 0755 "${DEPLOYMENT_ANCHOR}"
  printf '%s' "${RUN_TOKEN}" >"${ANCHOR_MARKER}"
  chown root:root "${ANCHOR_MARKER}"
  chmod 0600 "${ANCHOR_MARKER}"
  stat --format '%d:%i:%u:%g:%a:%F' "${DEPLOYMENT_ANCHOR}" >"${STATE_DIRECTORY}/anchor-identity"
  chmod 0600 "${STATE_DIRECTORY}/anchor-identity"
  install -d -o root -g root -m 0700 "${SECRET_DIRECTORY}" "${work}"

  postgres_password="rigpg-${RUN_TOKEN}-$(openssl rand -hex 24)"
  github_client_secret="rigclient-${RUN_TOKEN}-$(openssl rand -hex 24)"
  github_webhook_secret="rigwebhook-${RUN_TOKEN}-$(openssl rand -hex 24)"
  relay_postgres_dsn="postgresql://rig_relay:${postgres_password}@postgres:5432/rig_relay?sslmode=disable"

  printf '%s' "${postgres_password}" >"${work}/postgres-password.txt"
  printf '%s' "${relay_postgres_dsn}" >"${work}/relay-postgres-dsn.txt"
  printf '%s' "${github_client_secret}" >"${work}/github-client-secret.txt"
  printf '%s' "${github_webhook_secret}" >"${work}/github-webhook-secret.txt"
  openssl rand 32 >"${work}/enrollment-key.bin"
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${work}/github-app-private-key.pem" >/dev/null 2>&1

  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${work}/ca-private-key.pem" >/dev/null 2>&1
  openssl req -x509 -new -sha256 -days 2 \
    -key "${work}/ca-private-key.pem" \
    -subj "/CN=Rig relay lifecycle CI CA ${RUN_TOKEN}" \
    -out "${work}/relay-tls-ca.pem" >/dev/null 2>&1
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${work}/relay-tls-private-key.pem" >/dev/null 2>&1
  openssl req -new -sha256 \
    -key "${work}/relay-tls-private-key.pem" \
    -subj "/CN=${tls_name}" \
    -addext "subjectAltName=DNS:${tls_name}" \
    -out "${work}/relay-tls.csr" >/dev/null 2>&1
  printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "${tls_name}" >"${work}/relay-tls.ext"
  openssl x509 -req -sha256 -days 2 \
    -in "${work}/relay-tls.csr" \
    -CA "${work}/relay-tls-ca.pem" \
    -CAkey "${work}/ca-private-key.pem" \
    -CAcreateserial \
    -extfile "${work}/relay-tls.ext" \
    -out "${work}/relay-tls-certificate.pem" >/dev/null 2>&1

  install -o 999 -g 999 -m 0400 "${work}/postgres-password.txt" "${SECRET_DIRECTORY}/postgres-password.txt"
  install -o 65532 -g 65532 -m 0400 "${work}/relay-postgres-dsn.txt" "${SECRET_DIRECTORY}/relay-postgres-dsn.txt"
  install -o 65532 -g 65532 -m 0400 "${work}/github-client-secret.txt" "${SECRET_DIRECTORY}/github-client-secret.txt"
  install -o 65532 -g 65532 -m 0400 "${work}/github-app-private-key.pem" "${SECRET_DIRECTORY}/github-app-private-key.pem"
  install -o 65532 -g 65532 -m 0400 "${work}/github-webhook-secret.txt" "${SECRET_DIRECTORY}/github-webhook-secret.txt"
  install -o 65532 -g 65532 -m 0400 "${work}/enrollment-key.bin" "${SECRET_DIRECTORY}/enrollment-key.bin"
  install -o 65532 -g 65532 -m 0400 "${work}/relay-tls-certificate.pem" "${SECRET_DIRECTORY}/relay-tls-certificate.pem"
  install -o 65532 -g 65532 -m 0400 "${work}/relay-tls-private-key.pem" "${SECRET_DIRECTORY}/relay-tls-private-key.pem"
  install -o 65532 -g 65532 -m 0400 "${work}/relay-tls-ca.pem" "${SECRET_DIRECTORY}/relay-tls-ca.pem"

  enrollment_hex="$(od -An -v -tx1 "${work}/enrollment-key.bin" | tr -d ' \n')"
  enrollment_base64="$(base64 -w 0 "${work}/enrollment-key.bin")"
  : >"${private_key_patterns}"
  for key_file in github-app-private-key.pem relay-tls-private-key.pem ca-private-key.pem; do
    extracted_patterns="${STATE_DIRECTORY}/${key_file}.patterns"
    if ! sed -n '/BEGIN .*PRIVATE KEY/,/END .*PRIVATE KEY/p' "${work}/${key_file}" |
      grep -v -- '-----' >"${extracted_patterns}"; then
      fail "private-key leak pattern extraction failed"
    fi
    payload_count="$(awk 'NF { count++ } END { print count + 0 }' "${extracted_patterns}")"
    if [[ ! "${payload_count}" =~ ^[0-9]+$ ]] || (( payload_count < 1 )); then
      fail "private-key leak pattern set is empty"
    fi
    cat "${extracted_patterns}" >>"${private_key_patterns}"
  done
  {
    printf '%s\n' "${postgres_password}" "${relay_postgres_dsn}" "${github_client_secret}" "${github_webhook_secret}"
    printf '%s\n' "${enrollment_hex}" "${enrollment_base64}"
    printf '%s\n' '-----BEGIN PRIVATE KEY-----' '-----BEGIN RSA PRIVATE KEY-----'
    cat "${private_key_patterns}"
  } | awk 'NF && !seen[$0]++' >"${STATE_DIRECTORY}/secret-patterns"
  chmod 0600 "${STATE_DIRECTORY}/secret-patterns"
  : >"${STATE_DIRECTORY}/patterns-ready"
  chmod 0600 "${STATE_DIRECTORY}/patterns-ready"

  cat >"${STATE_DIRECTORY}/environment-template" <<EOF
HOSTD_RELAY_PUBLIC_BASE_URL=https://${tls_name}
HOSTD_RELAY_GITHUB_CLIENT_ID=Iv1.ci_${RUN_TOKEN}
HOSTD_RELAY_GITHUB_APP_ID=123456
HOSTD_RELAY_TLS_SERVER_NAME=${tls_name}
HOSTD_RELAY_EDGE_NETWORK=${EDGE_NETWORK}
HOSTD_RELAY_SECRET_DIRECTORY=${SECRET_DIRECTORY}
HOSTD_RELAY_PUBLISH_ADDRESS=127.0.0.1
HOSTD_RELAY_PUBLISH_PORT=7346
EOF
  chmod 0600 "${STATE_DIRECTORY}/environment-template"
}

write_environment() {
  local relay_image="$1"
  {
    printf 'HOSTD_RELAY_IMAGE=%s\n' "${relay_image}"
    cat "${STATE_DIRECTORY}/environment-template"
  } >"${ENVIRONMENT_FILE}"
  chown root:root "${ENVIRONMENT_FILE}"
  chmod 0600 "${ENVIRONMENT_FILE}"
}

build_relay_image() {
  local repo_digest
  prepare_required_log "${STATE_DIRECTORY}/image-build.log"
  docker build \
    --file "${REPOSITORY_ROOT}/deploy/relay/Dockerfile" \
    --platform linux/amd64 \
    --tag "${RELAY_TAG}" \
    --build-arg VERSION="ci-${RUN_TOKEN}" \
    --build-arg REVISION="${RUN_TOKEN}" \
    --build-arg CREATED="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "${REPOSITORY_ROOT}" >"${STATE_DIRECTORY}/image-build.log" 2>&1
  prepare_required_log "${STATE_DIRECTORY}/image-push.log"
  docker push "${RELAY_TAG}" >"${STATE_DIRECTORY}/image-push.log" 2>&1
  repo_digest="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${RELAY_TAG}" |
    grep -E '^127\.0\.0\.1:5000/rig-relay@sha256:[0-9a-f]{64}$' | head -n 1 || true)"
  if [[ -z "${repo_digest}" ]]; then
    repo_digest="127.0.0.1:5000/rig-relay@$(sed -nE 's/^.*digest: (sha256:[0-9a-f]{64}).*$/\1/p' "${STATE_DIRECTORY}/image-push.log" | tail -n 1)"
  fi
  [[ "${repo_digest}" =~ ^127\.0\.0\.1:5000/rig-relay@sha256:[0-9a-f]{64}$ ]] || fail "immutable relay RepoDigest unavailable"
  prepare_required_log "${STATE_DIRECTORY}/image-pull.log"
  docker pull "${repo_digest}" >"${STATE_DIRECTORY}/image-pull.log" 2>&1
  scan_captured_logs
  printf '%s' "${repo_digest}"
}

assert_healthy() {
  local service
  local container_id
  local health
  for service in postgres relay; do
    container_id="$(compose ps --quiet "${service}")"
    [[ -n "${container_id}" ]] || fail "service container unavailable"
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "${container_id}")"
    [[ "${health}" == "healthy" ]] || fail "service is not healthy"
  done
}

probe_relay() {
  local phase="$1"
  local endpoint
  local log_file="${STATE_DIRECTORY}/probe-${phase}.log"
  local tls_name="relay-${RUN_TOKEN}.ci.invalid"
  prepare_required_log "${log_file}"
  : >"${log_file}"
  for endpoint in health ready; do
    compose exec -T relay /usr/local/bin/rig-relay-probe \
      --base-url=https://127.0.0.1:7346 \
      --server-name="${tls_name}" \
      --ca-file=/run/secrets/relay_tls_ca \
      --endpoint="${endpoint}" \
      --timeout=5s >>"${log_file}" 2>&1
  done
  chmod 0600 "${log_file}"
}

query_migrations() {
  local destination="$1"
  local error_log="${destination}.stderr.log"
  prepare_required_log "${error_log}"
  compose exec -T postgres \
    psql --no-psqlrc --username=rig_relay --dbname=rig_relay --tuples-only --no-align \
    --set ON_ERROR_STOP=1 --field-separator='|' \
    --command="SELECT version, encode(checksum, 'hex'), to_char(applied_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM relay_schema_migrations ORDER BY version" \
    >"${destination}" 2>"${error_log}"
  chmod 0600 "${destination}"
  chmod 0600 "${error_log}"
}

assert_exact_migrations() {
  local actual_entries="${STATE_DIRECTORY}/migration-entries"
  local actual_names="${STATE_DIRECTORY}/migration-versions"
  local expected_entries="${STATE_DIRECTORY}/expected-migration-entries"
  local expected_names="${STATE_DIRECTORY}/expected-migration-versions"
  local source_names="${STATE_DIRECTORY}/source-migration-versions"
  local line
  local name
  local checksum
  local count=0
  local -a expected=(
    001_relay_state.sql
    002_enrollment_replay.sql
    003_recovery_attempt_groups.sql
    004_ignored_deliveries.sql
    005_wss_commands.sql
    006_wss_hardening.sql
    007_enrollment_capacity_retention.sql
  )

  printf '%s\n' "${expected[@]}" >"${expected_names}"
  find "${MIGRATION_DIRECTORY}" -maxdepth 1 -type f -name '*.sql' -printf '%f\n' | sort >"${source_names}"
  cmp --silent "${expected_names}" "${source_names}" || fail "embedded migration file set changed"
  : >"${expected_entries}"
  for name in "${expected[@]}"; do
    [[ -f "${MIGRATION_DIRECTORY}/${name}" && ! -L "${MIGRATION_DIRECTORY}/${name}" ]] ||
      fail "embedded migration source unavailable"
    checksum="$(sha256sum "${MIGRATION_DIRECTORY}/${name}" | cut -d ' ' -f 1)"
    [[ "${checksum}" =~ ^[0-9a-f]{64}$ ]] || fail "embedded migration checksum unavailable"
    printf '%s|%s\n' "${name}" "${checksum}" >>"${expected_entries}"
  done

  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ "${line}" =~ ^[0-9]{3}_[a-z0-9_]+\.sql\|[0-9a-f]{64}\|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$ ]] ||
      fail "migration ledger row is malformed"
    count=$((count + 1))
  done <"${STATE_DIRECTORY}/migrations-before"
  (( count == ${#expected[@]} )) || fail "migration ledger count changed"
  cut -d '|' -f 1 "${STATE_DIRECTORY}/migrations-before" >"${actual_names}"
  cut -d '|' -f 1,2 "${STATE_DIRECTORY}/migrations-before" >"${actual_entries}"
  cmp --silent "${expected_names}" "${actual_names}" || fail "migration ledger versions differ from embedded migrations"
  cmp --silent "${expected_entries}" "${actual_entries}" || fail "migration ledger checksums differ from embedded migrations"
}

create_persistence_marker() {
  local log_file="${STATE_DIRECTORY}/persistence-marker-create.log"
  prepare_required_log "${log_file}"
  compose exec -T postgres \
    psql --no-psqlrc --username=rig_relay --dbname=rig_relay --quiet \
    --set ON_ERROR_STOP=1 --set marker="${RUN_TOKEN}" >"${log_file}" 2>&1 <<'SQL'
CREATE TABLE rig_ci_lifecycle_marker (
  marker text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO rig_ci_lifecycle_marker(marker) VALUES (:'marker');
SQL
  chmod 0600 "${log_file}"
}

assert_persistence_marker() {
  local phase="$1"
  local count
  local error_log="${STATE_DIRECTORY}/persistence-marker-${phase}.stderr.log"
  prepare_required_log "${error_log}"
  count="$(compose exec -T postgres \
    psql --no-psqlrc --username=rig_relay --dbname=rig_relay --tuples-only --no-align \
    --set ON_ERROR_STOP=1 --set marker="${RUN_TOKEN}" \
    --command="SELECT count(*) FROM rig_ci_lifecycle_marker WHERE marker = :'marker'" 2>"${error_log}")"
  chmod 0600 "${error_log}"
  [[ "${count}" == "1" ]] || fail "PostgreSQL persistence marker changed"
}

query_system_identity() {
  local phase="$1"
  local error_log="${STATE_DIRECTORY}/postgres-system-${phase}.stderr.log"
  local identity
  prepare_required_log "${error_log}"
  identity="$(compose exec -T postgres \
    psql --no-psqlrc --username=rig_relay --dbname=rig_relay --tuples-only --no-align \
    --set ON_ERROR_STOP=1 --command='SELECT system_identifier FROM pg_control_system()' 2>"${error_log}")"
  chmod 0600 "${error_log}"
  [[ "${identity}" =~ ^[0-9]+$ ]] || fail "PostgreSQL system identity unavailable"
  printf '%s' "${identity}"
}

run_lifecycle() {
  local relay_image
  local postgres_before
  local postgres_after
  local relay_before
  local relay_after
  local volume_before
  local volume_after
  local system_before
  local system_after
  local sanitized_error
  local network_summary
  local service_summary
  local service_secret_summary
  local compose_version
  local diagnostic_line

  [[ "$(uname -s)" == "Linux" ]] || fail "hosted Linux is required"
  [[ "${CI:-}" == "true" && "${GITHUB_ACTIONS:-}" == "true" ]] || fail "hosted CI guard unavailable"
  require_commands
  trap on_exit EXIT
  trap on_error ERR
  trap 'exit 130' INT
  trap 'exit 143' TERM HUP
  CURRENT_PHASE="runtime-scope"
  assert_runtime_scope_empty
  CURRENT_PHASE="protected-fixture"
  create_fixture
  CURRENT_PHASE="isolated-registry"
  create_registry

  CURRENT_PHASE="edge-network"
  docker network create "${EDGE_NETWORK}" >"${STATE_DIRECTORY}/edge-network-id"
  [[ "$(docker network inspect --format '{{.Id}}' "${EDGE_NETWORK}")" == "$(<"${STATE_DIRECTORY}/edge-network-id")" ]] ||
    fail "edge network identity unavailable"

  CURRENT_PHASE="relay-image"
  relay_image="$(build_relay_image)"
  CURRENT_PHASE="deployment-environment"
  write_environment "${relay_image}"

  CURRENT_PHASE="packaging-preflight"
  prepare_required_log "${STATE_DIRECTORY}/packaging-preflight.log"
  if ! pwsh -NoProfile -File "${REPOSITORY_ROOT}/scripts/check-relay-packaging.ps1" \
    -TrustedDeploymentAnchor "${DEPLOYMENT_ANCHOR}" \
    -EnvironmentFile "${ENVIRONMENT_FILE}" \
    -SecretDirectory "${SECRET_DIRECTORY}" \
    -DeploymentMode baseline >"${STATE_DIRECTORY}/packaging-preflight.log" 2>&1; then
    sanitized_error="$(grep --only-matching --extended-regexp 'relay packaging check failed: [a-z0-9_, ]+' \
      "${STATE_DIRECTORY}/packaging-preflight.log" | tail -n 1 || true)"
    if [[ -n "${sanitized_error}" ]]; then
      printf '%s\n' "${sanitized_error}" >&2
    fi
    if [[ "${sanitized_error}" == *"compose_effective_networks"* ]]; then
      if network_summary="$(docker compose --env-file "${ENVIRONMENT_FILE}" --file "${COMPOSE_FILE}" \
        config --format json 2>/dev/null | jq --compact-output \
        '.networks | to_entries | map({key: .key, fields: (.value | keys | sort), internal: (.value.internal // false), external: (.value.external // false), name: .value.name})')"; then
        if [[ -n "${network_summary}" && "${network_summary}" != *$'\n'* ]] &&
           (( ${#network_summary} <= 2048 )) &&
           compose_version="$(docker compose version --short 2>/dev/null)" &&
           [[ -n "${compose_version}" && "${compose_version}" != *$'\n'* ]]; then
          diagnostic_line="relay Compose lifecycle network summary: compose=${compose_version} model=${network_summary}"
          if [[ "${diagnostic_line}" != *$'\n'* ]] && (( ${#diagnostic_line} <= 2048 )); then
            printf '%s\n' "${diagnostic_line}" >&2
          fi
        fi
      fi
    fi
    if [[ "${sanitized_error}" == *"compose_effective_service_keys"* ]]; then
      if service_summary="$(docker compose --env-file "${ENVIRONMENT_FILE}" --file "${COMPOSE_FILE}" \
        config --format json 2>/dev/null | jq --compact-output \
        '{postgres: (.services.postgres | keys | sort), relay: (.services.relay | keys | sort)}')"; then
        if [[ -n "${service_summary}" && "${service_summary}" != *$'\n'* ]] &&
           (( ${#service_summary} <= 2048 )) &&
           compose_version="$(docker compose version --short 2>/dev/null)" &&
           [[ -n "${compose_version}" && "${compose_version}" != *$'\n'* ]]; then
          diagnostic_line="relay Compose lifecycle service-key summary: compose=${compose_version} model=${service_summary}"
          if [[ "${diagnostic_line}" != *$'\n'* ]] && (( ${#diagnostic_line} <= 2048 )); then
            printf '%s\n' "${diagnostic_line}" >&2
          fi
        fi
      fi
    fi
    if [[ "${sanitized_error}" == *"compose_effective_service_secrets"* ]]; then
      if service_secret_summary="$(docker compose --env-file "${ENVIRONMENT_FILE}" --file "${COMPOSE_FILE}" \
        config --format json 2>/dev/null | jq --compact-output '
          def safe_field_keys:
            [keys_unsorted[]
             | if . == "source" or . == "target" or . == "uid" or . == "gid" or . == "mode"
               then .
               else "other"
               end]
            | unique | sort;
          def service_secrets($service):
            (.services[$service].secrets? // []) as $raw
            | ($raw | if type == "array" then . else [] end) as $attachments
            | {
                attachment_count: ($attachments | length),
                field_key_sets: ([$attachments[] | select(type == "object") | safe_field_keys] | unique | sort),
                string_source_count: ([$attachments[] | select(type == "object") | select((.source? | type) == "string")] | length),
                string_target_count: ([$attachments[] | select(type == "object") | select((.target? | type) == "string")] | length),
                exact_run_secrets_target_count: ([$attachments[] | select(type == "object") | select((.source? | type) == "string" and (.target? | type) == "string") | select(.target == ("/run/secrets/" + .source))] | length),
                absolute_run_secrets_target_count: ([$attachments[] | select(type == "object") | select((.target? | type) == "string") | select(.target | startswith("/run/secrets/"))] | length)
              };
          {postgres: service_secrets("postgres"), relay: service_secrets("relay")}
        ')"; then
        if [[ -n "${service_secret_summary}" && "${service_secret_summary}" != *$'\n'* ]] &&
           (( ${#service_secret_summary} <= 1900 )) &&
           compose_version="$(docker compose version --short 2>/dev/null)" &&
           [[ -n "${compose_version}" && "${compose_version}" != *$'\n'* && "${compose_version}" =~ ^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}([.-][0-9A-Za-z.-]{1,32})?$ ]] &&
           (( ${#compose_version} <= 64 )); then
          diagnostic_line="relay Compose lifecycle service-secret shape: compose=${compose_version} model=${service_secret_summary}"
          if [[ -n "${diagnostic_line}" && "${diagnostic_line}" != *$'\n'* ]] && (( ${#diagnostic_line} <= 2048 )); then
            printf '%s\n' "${diagnostic_line}" >&2
          fi
        fi
      fi
    fi
    fail "packaging preflight rejected the isolated fixture"
  fi
  chmod 0600 "${STATE_DIRECTORY}/packaging-preflight.log"
  scan_captured_logs

  CURRENT_PHASE="compose-start-one"
  : >"${STATE_DIRECTORY}/compose-attempted"
  chmod 0600 "${STATE_DIRECTORY}/compose-attempted"
  prepare_required_log "${STATE_DIRECTORY}/compose-up-one.log"
  compose up --detach --wait --wait-timeout 180 >"${STATE_DIRECTORY}/compose-up-one.log" 2>&1
  chmod 0600 "${STATE_DIRECTORY}/compose-up-one.log"
  CURRENT_PHASE="health-one"
  assert_healthy
  CURRENT_PHASE="probe-one"
  probe_relay before

  CURRENT_PHASE="state-one"
  postgres_before="$(compose ps --quiet postgres)"
  relay_before="$(compose ps --quiet relay)"
  volume_before="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql"}}{{.Name}}{{end}}{{end}}' "${postgres_before}")"
  [[ -n "${volume_before}" ]] || fail "PostgreSQL volume unavailable"
  docker volume inspect "${volume_before}" >/dev/null
  system_before="$(query_system_identity before)"

  CURRENT_PHASE="migrations-one"
  query_migrations "${STATE_DIRECTORY}/migrations-before"
  assert_exact_migrations
  sha256sum "${STATE_DIRECTORY}/migrations-before" | cut -d ' ' -f 1 >"${STATE_DIRECTORY}/migrations-before.sha256"
  CURRENT_PHASE="persistence-marker"
  create_persistence_marker
  CURRENT_PHASE="logs-one"
  capture_logs "${STATE_DIRECTORY}/generation-one.log"
  scan_captured_logs

  CURRENT_PHASE="compose-start-two"
  prepare_required_log "${STATE_DIRECTORY}/compose-up-two.log"
  compose up --detach --wait --wait-timeout 180 --no-deps --force-recreate relay \
    >"${STATE_DIRECTORY}/compose-up-two.log" 2>&1
  chmod 0600 "${STATE_DIRECTORY}/compose-up-two.log"
  CURRENT_PHASE="health-two"
  assert_healthy
  CURRENT_PHASE="probe-two"
  probe_relay after

  CURRENT_PHASE="state-two"
  postgres_after="$(compose ps --quiet postgres)"
  relay_after="$(compose ps --quiet relay)"
  volume_after="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql"}}{{.Name}}{{end}}{{end}}' "${postgres_after}")"
  system_after="$(query_system_identity after)"

  [[ "${relay_after}" != "${relay_before}" ]] || fail "relay container was not recreated"
  [[ "${postgres_after}" == "${postgres_before}" ]] || fail "PostgreSQL container changed"
  [[ "${volume_after}" == "${volume_before}" ]] || fail "PostgreSQL volume changed"
  [[ "${system_after}" == "${system_before}" ]] || fail "PostgreSQL system identity changed"
  assert_persistence_marker after

  CURRENT_PHASE="migrations-two"
  query_migrations "${STATE_DIRECTORY}/migrations-after"
  cmp --silent "${STATE_DIRECTORY}/migrations-before" "${STATE_DIRECTORY}/migrations-after" ||
    fail "migration ledger changed during relay recreation"
  sha256sum "${STATE_DIRECTORY}/migrations-after" | cut -d ' ' -f 1 >"${STATE_DIRECTORY}/migrations-after.sha256"
  cmp --silent "${STATE_DIRECTORY}/migrations-before.sha256" "${STATE_DIRECTORY}/migrations-after.sha256" ||
    fail "migration ledger hash changed during relay recreation"
  CURRENT_PHASE="logs-two"
  capture_logs "${STATE_DIRECTORY}/generation-two.log"
  scan_captured_logs
  CURRENT_PHASE="complete"
  printf 'relay Compose lifecycle check passed\n'
}

valid_scope
require_root
case "${ACTION}" in
  run)
    run_lifecycle
    ;;
  cleanup)
    cleanup 0
    ;;
  *)
    fail "usage: check-relay-compose-lifecycle.sh run|cleanup RUN_ID-RUN_ATTEMPT"
    ;;
esac
