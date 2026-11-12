#!/usr/bin/env bash
# Copyright 2026 Lingfei Kong <colin404@foxmail.com>
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Local acceptance run for oai-agent-apiserver.
#
# It answers one question: "does this checkout still do the things we verified
# by hand?" Every check here was found by hand first — several of them after a
# bug that unit tests could not see — and this script is how the next change is
# held to the same standard without re-deriving the recipe each time.
#
#   hack/acceptance.sh            # checks everything it can on this machine
#   hack/acceptance.sh --no-services   # skip the parts that start servers
#
# Requirements: a built tree, the local control plane (declserver) up, and a
# config at ~/.onexai/oai-agent-apiserver.yaml. The script never edits that
# config: it starts its own instance with flags that override it.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${ROOT}/_output/platforms/$(go env GOOS)/$(go env GOARCH)"
CONFIG="${HOME}/.onexai/oai-agent-apiserver.yaml"
PUBLIC_ADDR=":9182"
INTERNAL_ADDR="127.0.0.1:8092"
AGENT="fz-public"
SUBJECT="acceptance"

RUN_SERVICES=1
case "${1:-}" in
--no-services) RUN_SERVICES=0 ;;
"") ;;
*)
  echo "usage: $0 [--no-services]" >&2
  exit 2
  ;;
esac

pass=0
fail=0
note() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok() {
  printf '  \033[32mok\033[0m   %s\n' "$*"
  pass=$((pass + 1))
}
bad() {
  printf '  \033[31mFAIL\033[0m %s\n' "$*"
  fail=$((fail + 1))
}

cleanup() {
  if [[ -n "${INSTANCE_PID:-}" ]]; then
    kill "${INSTANCE_PID}" 2>/dev/null || true
    wait "${INSTANCE_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ---------------------------------------------------------------- build + unit

note "build"
if (cd "$ROOT" && make build >/dev/null 2>&1); then
  ok "make build"
else
  bad "make build"
  exit 1
fi

note "unit tests (the packages this service's behaviour lives in)"
# CGO is off because go-m1cpu segfaults in init on this machine, and the
# sqlstore sqlite tests cannot run without it — they are excluded by package.
if (cd "$ROOT" && CGO_ENABLED=0 go test \
  ./internal/agent/pkg/a2a/ \
  ./internal/agent/pkg/a2awire/ \
  ./internal/agent/pkg/agentedge/ \
  ./internal/agent/pkg/runtime/... \
  ./internal/agent/pkg/outputschema/ \
  ./internal/agent/pkg/telemetry/ \
  ./internal/agent/pkg/observe/ \
  ./internal/agent/runtimecli/ \
  ./internal/agent/capability/ \
  ./internal/agent/biz/v1/agent/ \
  ./internal/agent/biz/v1/schedule/ \
  ./internal/agent/handler/ \
  ./internal/agent/di/ \
  ./internal/pkg/authz/scope/ \
  ./pkg/apis/agent/validation/ >/dev/null 2>&1); then
  ok "go test"
else
  bad "go test"
fi

if [[ "$RUN_SERVICES" -eq 0 ]]; then
  printf '  %d passed, %d failed (services skipped)\n' "$pass" "$fail"
  [[ "$fail" -eq 0 ]]
  exit
fi

# ------------------------------------------------------------------- services

note "local services"
# The declserver's health endpoint is HTTPS with a self-signed certificate; a
# plain http probe gets a 400 and looks like "not running".
if curl -sfk -o /dev/null --max-time 3 https://127.0.0.1:6444/healthz; then
  ok "declserver is up"
else
  bad "declserver is not answering on :6444 (onexai cmp start declserver)"
  exit 1
fi

# A separate instance with authentication bypassed: the tests below drive the
# HTTP surface directly, and the live config requires an AK/SK signature.
# The internal listener moves because the running service owns 8091.
if [[ ! -f "$CONFIG" ]]; then
  bad "missing $CONFIG"
  exit 1
fi
"${BIN}/oai-agent-apiserver" \
  --config "$CONFIG" \
  --insecure.addr="$PUBLIC_ADDR" \
  --auth.bypass-aksk=true \
  --authz.enabled=false \
  --agent.runtime.internal-addr="$INTERNAL_ADDR" \
  --agent.sandbox.enabled=true >/tmp/acceptance-agent.log 2>&1 &
INSTANCE_PID=$!

for _ in $(seq 1 40); do
  if curl -sf -o /dev/null --max-time 2 "http://127.0.0.1${PUBLIC_ADDR}/readyz"; then break; fi
  sleep 2
done
if curl -sf -o /dev/null --max-time 2 "http://127.0.0.1${PUBLIC_ADDR}/readyz"; then
  ok "acceptance instance is ready"
else
  bad "acceptance instance did not become ready; see /tmp/acceptance-agent.log"
  exit 1
fi

# ------------------------------------------------------------------- eino run

note "eino run, revision and settlement"
SESSION="acceptance-$(date +%s)"
ANSWER=$(curl -s -N -X POST "http://127.0.0.1${PUBLIC_ADDR}/v1/agent/ask" \
  -H 'Content-Type: application/json' \
  -d "{\"sessionId\":\"${SESSION}\",\"input\":{\"text\":\"只回答数字：2+3=?\"},
       \"selection\":{\"agents\":[\"${AGENT}\"]},\"subject\":{\"id\":\"${SUBJECT}\",\"name\":\"${SUBJECT}\"}}" \
  --max-time 120 | grep -o '"final":"[^"]*"' | tail -1 | cut -d'"' -f4 || true)
if [[ -n "$ANSWER" ]]; then ok "answer: ${ANSWER}"; else bad "no final answer"; fi

RUN_ID=$(curl -s "http://127.0.0.1${PUBLIC_ADDR}/v1/agent/sessions/${SESSION}/runs" |
  python3 -c 'import json,sys; runs=json.load(sys.stdin).get("runs") or []; print(runs[0]["id"] if runs else "")')
if [[ -n "$RUN_ID" ]]; then
  RUN_JSON=$(curl -s "http://127.0.0.1${PUBLIC_ADDR}/v1/agent/runs/${RUN_ID}")
  STATE=$(printf '%s' "$RUN_JSON" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("run") or {}).get("state",""))')
  REV=$(printf '%s' "$RUN_JSON" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("run") or {}).get("revision_id",""))')
  RUNTIME=$(printf '%s' "$RUN_JSON" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("run") or {}).get("runtime",""))')
  [[ "$STATE" == "completed" ]] && ok "run recorded as completed" || bad "run state is ${STATE}"
  [[ -n "$REV" ]] && ok "run carries a revision" || bad "run has no revision_id"
  [[ "$RUNTIME" == "eino" ]] && ok "run recorded runtime=eino" || bad "run runtime is ${RUNTIME}"
else
  bad "the session has no run row"
fi

# ---------------------------------------------------------------------- share

note "session share"
TOKEN=$(curl -s -X POST "http://127.0.0.1${PUBLIC_ADDR}/v1/agent/sessions/${SESSION}/shares?ttlSeconds=600" |
  python3 -c 'import json,sys; print(json.load(sys.stdin).get("token",""))')
if [[ -n "$TOKEN" ]]; then
  ok "share token issued"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' \
    "http://127.0.0.1${PUBLIC_ADDR}/v1/share/sessions/${SESSION}/runs?token=${TOKEN}")
  [[ "$CODE" == "200" ]] && ok "shared read returns 200" || bad "shared read returned ${CODE}"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' \
    "http://127.0.0.1${PUBLIC_ADDR}/v1/share/sessions/not-this-session/runs?token=${TOKEN}")
  [[ "$CODE" == "403" ]] && ok "a share does not cover another session" || bad "wrong session returned ${CODE}"
else
  bad "no share token"
fi

# ------------------------------------------------------------------- a2a face

note "a2a surface"
CARD="http://127.0.0.1${PUBLIC_ADDR}/.well-known/agent-card.json"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$CARD")
[[ "$CODE" == "200" ]] && ok "agent card is public at the origin root" || bad "agent card returned ${CODE}"

CODE=$(curl -s -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1${PUBLIC_ADDR}/v1/chat/a2a/.well-known/agent-card.json")
[[ "$CODE" == "404" ]] && ok "the old nested card path is gone" || bad "old card path returned ${CODE}"

# -------------------------------------------------------------- capability gw

note "capability gateway"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://${INTERNAL_ADDR}/v1/mcp" \
  -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
[[ "$CODE" == "401" ]] && ok "an unsigned capability call is refused" || bad "unsigned call returned ${CODE}"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://${INTERNAL_ADDR}/v1/mcp" \
  -H 'Authorization: Bearer forged' -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
[[ "$CODE" == "401" ]] && ok "a forged token is refused" || bad "forged token returned ${CODE}"

# ------------------------------------------------------------------- sandbox

note "sandbox (only when docker and a template are available)"
TEMPLATE=$(kubectl --kubeconfig "${HOME}/.onexai/declserver.kubeconfig" get sandboxtemplates -A \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "  note: docker is not available; skipped"
elif [[ -z "$TEMPLATE" ]]; then
  echo "  note: no SandboxTemplate exists; skipped (see ~/new.md 2.8)"
else
  SANDBOX=$(curl -s -X POST "http://127.0.0.1${PUBLIC_ADDR}/v1/sandboxes" -H 'Content-Type: application/json' \
    -d "{\"owner\":\"${SUBJECT}\",\"template\":\"${TEMPLATE}\",\"ttlSeconds\":120}" |
    python3 -c 'import json,sys; print((json.load(sys.stdin).get("sandbox") or {}).get("id",""))')
  if [[ -n "$SANDBOX" ]]; then
    ok "sandbox created from ${TEMPLATE}"
    OUTPUT=$(curl -s -X POST "http://127.0.0.1${PUBLIC_ADDR}/v1/sandboxes/${SANDBOX}/exec" \
      -H 'Content-Type: application/json' \
      -d "{\"owner\":\"${SUBJECT}\",\"command\":\"id -u && echo acceptance\"}" |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("output",""))')
    if printf '%s' "$OUTPUT" | grep -q acceptance; then
      ok "exec runs inside the sandbox"
    else
      bad "exec returned: ${OUTPUT}"
    fi
    printf 'notes' | curl -s -X PUT "http://127.0.0.1${PUBLIC_ADDR}/v1/sandboxes/${SANDBOX}/files" \
      -H 'Content-Type: application/json' \
      -d "{\"owner\":\"${SUBJECT}\",\"path\":\"notes.txt\",\"content\":\"persisted\"}" \
      -o /dev/null -w '' 2>/dev/null || true
    CONTENT=$(curl -s "http://127.0.0.1${PUBLIC_ADDR}/v1/sandboxes/${SANDBOX}/files?owner=${SUBJECT}&path=notes.txt" |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("content",""))')
    [[ "$CONTENT" == "persisted" ]] && ok "files round-trip inside the workspace" \
      || bad "file read returned: ${CONTENT}"
    CODE=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
      "http://127.0.0.1${PUBLIC_ADDR}/v1/sandboxes/${SANDBOX}?owner=${SUBJECT}")
    [[ "$CODE" == "204" ]] && ok "sandbox deleted" || bad "delete returned ${CODE}"
  else
    bad "sandbox could not be created from ${TEMPLATE}"
  fi
fi

# ------------------------------------------------------------ cli harness

note "cli harness (oai-agent-runtime)"
WORK=$(mktemp -d)
STUB="${WORK}/stub-claude.sh"
cat >"${STUB}" <<'STUB'
#!/bin/sh
# Reports a fixed session id and echoes its arguments, so the check can see
# whether --resume was passed without needing a real model.
printf '{"type":"system","subtype":"init","session_id":"stub-sess-1"}\n'
printf '{"type":"result","subtype":"success","is_error":false,"result":"argv: %s","session_id":"stub-sess-1"}\n' "$*"
STUB
chmod +x "${STUB}"
cat >"${WORK}/runtime.json" <<JSON
{"name":"claude","command":"${STUB}","args":["-p","{{prompt}}","--output-format","stream-json"],"format":"claude","sessionDir":"${WORK}/sessions"}
JSON

"${BIN}/oai-agent-runtime" --config "${WORK}/runtime.json" --addr 127.0.0.1:18098 >"${WORK}/runtime.log" 2>&1 &
RUNTIME_PID=$!
for _ in $(seq 1 20); do
  if curl -sf -o /dev/null --max-time 2 http://127.0.0.1:18098/.well-known/agent-card.json; then break; fi
  sleep 1
done

send_a2a() { # context, message
  curl -s -X POST http://127.0.0.1:18098/ -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"SendMessage\",\"params\":{\"message\":{\"messageId\":\"m-$(date +%s%N)\",\"role\":\"ROLE_USER\",\"contextId\":\"$1\",\"parts\":[{\"text\":\"$2\"}]}}}" \
    --max-time 60 | python3 -c 'import json,sys
d=json.load(sys.stdin); t=(d.get("result") or {}).get("task") or {}
print("".join("".join(p.get("text","") for p in (a.get("parts") or [])) for a in (t.get("artifacts") or [])))'
}

FIRST=$(send_a2a "acceptance-ctx" "hello")
if printf '%s' "$FIRST" | grep -q -- "-p hello"; then
  ok "the CLI harness runs the configured command"
else
  bad "first turn returned: ${FIRST}"
fi
if printf '%s' "$FIRST" | grep -q -- "--resume"; then
  bad "the first turn of a conversation must not resume"
else
  ok "a new conversation starts without --resume"
fi

SECOND=$(send_a2a "acceptance-ctx" "again")
if printf '%s' "$SECOND" | grep -q -- "--resume stub-sess-1"; then
  ok "a second turn resumes the conversation"
else
  bad "second turn returned: ${SECOND}"
fi

THIRD=$(send_a2a "acceptance-other" "elsewhere")
if printf '%s' "$THIRD" | grep -q -- "--resume"; then
  bad "a different conversation inherited the first one's session"
else
  ok "another conversation does not resume"
fi

kill "$RUNTIME_PID" 2>/dev/null || true
wait "$RUNTIME_PID" 2>/dev/null || true
rm -rf "$WORK"

note "result"
printf '  %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
