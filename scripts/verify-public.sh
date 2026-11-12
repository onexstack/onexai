#!/usr/bin/env bash
# Fail if the public tree contains private-looking material.
set -euo pipefail

fail=0

check() {
  local description="$1"; shift
  if "$@"; then
    echo "OK: $description"
  else
    echo "FAIL: $description" >&2
    fail=1
  fi
}

check "no legacy module path"   bash -c '! grep -rq "github.com/feizaonet/onexai" --include="*.go" --include="*.proto" --include="go.mod" . --exclude-dir=third_party --exclude-dir=_output --exclude=verify-public.sh'

check "no private CLI imports"   bash -c '! grep -rq "onexstack/onexai/internal/onexai" --include="*.go" . --exclude-dir=_output --exclude=verify-public.sh'

check "no absolute developer paths"   bash -c '! grep -rqE "/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/" --include="*.go" --include="*.yaml" --include="*.md" --include="*.sh" . --exclude-dir=third_party --exclude-dir=_output --exclude=verify-public.sh'

check "no internal hostnames"   bash -c '! grep -rqiE "infra-obs|infra-devops|ccr\.ccs\.tencentyun|sre-gitlab|bitget\.tools" --include="*.go" --include="*.yaml" --include="*.md" --include="*.sh" . --exclude-dir=third_party --exclude-dir=_output --exclude=verify-public.sh'

exit "$fail"
