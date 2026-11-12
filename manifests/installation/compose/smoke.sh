#!/usr/bin/env bash
#
# End-to-end smoke test for the compose stack.
#
#   ./smoke.sh
#
# It checks the infrastructure and every service, then (when LLM_BASE_URL and
# LLM_API_KEY are set) sends a real /v1/chat/completions request.
#
# Exit status is non-zero if any check fails. A missing LLM configuration is
# reported and skipped, not treated as a failure.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

if [[ -f env ]]; then
  # shellcheck disable=SC1091
  set -a; . ./env; set +a
  compose=(docker compose --env-file env -f compose.yaml)
else
  compose=(docker compose -f compose.yaml)
fi

pass=0
fail=0
note() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m   %s\n' "$*"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail + 1)); }
skip() { printf '  \033[33mskip\033[0m %s\n' "$*"; }

dc() { "${compose[@]}" "$@"; }

AGENT_URL="http://127.0.0.1:${AGENT_HOST_PORT:-8080}"
CHAT_URL="http://127.0.0.1:${CHAT_HOST_PORT:-8082}"
DECLSERVER_URL="https://127.0.0.1:${DECLSERVER_HOST_PORT:-6444}"
RUNTIME_URL="http://127.0.0.1:${RUNTIME_HOST_PORT:-8090}"

# wait_url <url> <label> [curl flags...]
wait_url() {
  local url=$1 label=$2; shift 2
  local i
  for i in $(seq 1 90); do
    if curl -fsS "$@" --max-time 3 "$url" >/dev/null 2>&1; then
      ok "$label"
      return 0
    fi
    sleep 2
  done
  bad "$label ($url)"
  return 1
}

# wait_exec <service> <url> <label>
# For services whose health port is not published to the host.
wait_exec() {
  local svc=$1 url=$2 label=$3
  local i
  for i in $(seq 1 90); do
    if dc exec -T "$svc" curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
      ok "$label"
      return 0
    fi
    sleep 2
  done
  bad "$label ($svc $url)"
  return 1
}

note "infrastructure"
dc exec -T etcd etcdctl --endpoints=http://127.0.0.1:2379 endpoint health >/dev/null 2>&1 \
  && ok "etcd is healthy" || bad "etcd is not healthy"
dc exec -T postgres pg_isready -U "${POSTGRES_USER:-onexai}" -d "${POSTGRES_DB:-onexai}" >/dev/null 2>&1 \
  && ok "postgres is ready" || bad "postgres is not ready"
dc exec -T redis redis-cli ping >/dev/null 2>&1 \
  && ok "redis is ready" || bad "redis is not ready"

note "control plane"
wait_url "${DECLSERVER_URL}/healthz" "declserver is healthy" -k || true
wait_exec "oai-controller-manager" "http://127.0.0.1:20250/healthz" "controller-manager is healthy" || true

note "services"
wait_url "${AGENT_URL}/readyz" "agent-apiserver is ready" || true
wait_url "${CHAT_URL}/readyz" "chat-apiserver is ready" || true
wait_exec "oai-job-controller" "http://127.0.0.1:8081/healthz" "job-controller is healthy" || true
wait_exec "oai-jobserver" "http://127.0.0.1:8881/healthz" "jobserver is healthy" || true
wait_url "${RUNTIME_URL}/.well-known/agent-card.json" "agent-runtime serves its A2A card" || true

note "database schemas"
schemas=$(dc exec -T postgres psql -U "${POSTGRES_USER:-onexai}" -d "${POSTGRES_DB:-onexai}" -Atc \
  "select schema_name from information_schema.schemata
    where schema_name in ('agent','chat','job') order by 1" 2>/dev/null || true)
if [[ "$(printf '%s\n' "$schemas" | tr -d '[:space:]')" == "agentchatjob" ]]; then
  ok "agent/chat/job schemas exist"
else
  bad "expected agent/chat/job schemas, got: ${schemas:-<none>}"
fi

note "caller credentials"
if [[ "${AUTH_BYPASS_AKSK:-true}" == "false" ]]; then
  secrets=$(dc exec -T postgres psql -U "${POSTGRES_USER:-onexai}" -d "${POSTGRES_DB:-onexai}" -Atc \
    "select secret_id from agent.secret
      where secret_id in ('oai-chat-apiserver','oai-job-controller') order by 1" 2>/dev/null || true)
  if [[ "$(printf '%s\n' "$secrets" | tr -d '[:space:]')" == "oai-chat-apiserveroai-job-controller" ]]; then
    ok "AK/SK rows are seeded"
  else
    bad "secure mode is on but the AK/SK rows are missing: ${secrets:-<none>}"
  fi
else
  skip "auth.bypass-aksk is on; caller credentials are not required"
fi

note "chat completion"
if [[ -z "${LLM_BASE_URL:-}" || -z "${LLM_API_KEY:-}" ]]; then
  skip "LLM_BASE_URL / LLM_API_KEY are empty; add them to env to test a real answer"
else
  body=$(printf '{"agent":"default","model":"%s","messages":[{"role":"user","content":"Reply with the single word: ok"}]}' "${LLM_MODEL:-gpt-4o-mini}")
  path=/v1/chat/completions

  if [[ -n "${CHAT_AKSK_SECRET:-}" ]]; then
    body_md5=$(printf '%s' "$body" | openssl dgst -md5 | awk '{print $NF}')
    ts=$(( $(date +%s) * 1000 ))
    nonce=$(openssl rand -hex 16)
    raw=$(printf '%s\n%s\n%s\n%s\n%s' POST "$path" "$body_md5" "$ts" "$nonce")
    sign=$(printf '%s' "$raw" | openssl dgst -sha256 -hmac "$CHAT_AKSK_SECRET" | awk '{print $NF}')
    response=$(curl -sS -X POST "${AGENT_URL}${path}" \
      -H 'Content-Type: application/json' \
      -H "X-API-KEY: ${CHAT_AKSK_API_KEY:-oai-chat-apiserver}" \
      -H "X-TIMESTAMP: ${ts}" \
      -H "X-NONCE: ${nonce}" \
      -H "X-SIGN: ${sign}" \
      --data-binary "$body" --max-time 180 || true)
  else
    response=$(curl -sS -X POST "${AGENT_URL}${path}" \
      -H 'Content-Type: application/json' \
      --data-binary "$body" --max-time 180 || true)
  fi

  if grep -q '"choices"' <<<"$response"; then
    ok "chat completion returned a choice"
  else
    bad "chat completion failed: ${response:0:400}"
  fi
fi

note "summary"
printf '  %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
