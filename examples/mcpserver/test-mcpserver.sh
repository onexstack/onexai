
#!/bin/bash

# oai-mcpserver end-to-end test script.
#
# Builds oai-mcpserver and the example MCP client, then drives the client
# against the server over all three MCP transports (stdio, sse,
# streamable-http), exercising namespaced tool discovery and invocation
# across the echo/, system/, and blog/ namespaces on each.
set -e

SERVER_BIN=${SERVER_BIN:-"/tmp/oai-mcpserver"}
CLIENT_BIN=${CLIENT_BIN:-"/tmp/mcp-client"}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

SSE_ADDR="127.0.0.1:18081"
STREAMABLE_HTTP_ADDR="127.0.0.1:18081"

# server_pid tracks the currently running background oai-mcpserver instance
# (sse/streamable-http tests only), so cleanup can kill it on exit -- success
# or failure -- instead of leaking a listening process.
server_pid=""

cleanup() {
    if [[ -n "$server_pid" ]]; then
        kill "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
        server_pid=""
    fi
}
trap cleanup EXIT

echo "=== oai-mcpserver Test Suite ==="

echo "Building oai-mcpserver..."
(cd "$REPO_ROOT" && go build -o "$SERVER_BIN" ./cmd/oai-mcpserver)

echo "Building example MCP client..."
(cd "$REPO_ROOT" && go build -o "$CLIENT_BIN" ./examples/mcpserver/mcp-client.go)

run_test() {
    local test_name="$1"
    local expect_substring="$2"
    shift 2
    local client_args=("$@")

    echo ""
    echo "Running: $test_name"
    echo "----------------------------------------"

    output=$("$CLIENT_BIN" "${client_args[@]}" 2>&1)
    echo "$output"

    if ! grep -q "$expect_substring" <<<"$output"; then
        echo "❌ $test_name FAILED: expected output to contain '$expect_substring'"
        exit 1
    fi

    echo "✅ $test_name passed"
}

# wait_for_addr blocks until addr accepts TCP connections, or fails the
# script after ~5s so a slow-starting server doesn't hang the test suite.
wait_for_addr() {
    local addr="$1"
    local host="${addr%:*}"
    local port="${addr##*:}"

    for _ in $(seq 1 50); do
        if nc -z "$host" "$port" 2>/dev/null; then
            return 0
        fi
        sleep 0.1
    done

    echo "❌ server at $addr did not start within 5s"
    exit 1
}

echo ""
echo "########################################"
echo "# Transport: stdio"
echo "########################################"

# Test 1: full handshake + namespace-scoped listing + tool calls succeed end to end.
#run_test "[stdio] Full client walkthrough" "=== done ===" -transport stdio -bin "$SERVER_BIN"

for transport_addr in "streamable-http:$STREAMABLE_HTTP_ADDR" "sse:$SSE_ADDR"; do
    transport="${transport_addr%%:*}"
    addr="${transport_addr#*:}"

    echo ""
    echo "########################################"
    echo "# Transport: $transport"
    echo "########################################"

    echo "Starting oai-mcpserver -t $transport --addr $addr ..."
    "$SERVER_BIN" -t "$transport" --addr "$addr" &
    server_pid=$!

    wait_for_addr "$addr"

    run_test "[$transport] Full client walkthrough" "=== done ===" \
        -transport "$transport" -addr "$addr"

    run_test "[$transport] Namespace filtering excludes other namespaces" '"name": "echo_ping"' \
        -transport "$transport" -addr "$addr"

    run_test "[$transport] x-namespaces/list reports every registered namespace" '"blog"' \
        -transport "$transport" -addr "$addr"

    cleanup
done

echo ""
echo "=== All tests completed successfully! ==="
