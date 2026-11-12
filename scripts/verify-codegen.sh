#!/usr/bin/env bash
# Verify that checked-in generated code matches its sources.
#
# Regenerates everything codegen owns, then fails if the working tree changed.
# CI runs this so a hand-edited or stale generated file cannot land on main.
#
# k8s.io/code-generator is deterministic by construction, which is what makes
# the diff check meaningful: it emits stable output for the same input.
#
# Usage:
#   bash scripts/verify-codegen.sh
#   make verify-codegen
set -o errexit
set -o nounset
set -o pipefail

PROJ_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${PROJ_ROOT}"

# Paths owned by codegen. Keep in sync with scripts/update-codegen.sh.
GENERATED_PATHS=(
  pkg/apis
  pkg/config
  pkg/generated
  internal/controller/apis/config
)

echo "===========> Regenerating Kubernetes API code"
bash scripts/update-codegen.sh

echo "===========> Checking for drift"
changed="$(git status --porcelain -- "${GENERATED_PATHS[@]}")"
if [[ -n "${changed}" ]]; then
  echo "Generated code is out of date:" >&2
  echo "${changed}" >&2
  echo >&2
  echo "Run 'make generate' and 'make gen-k8s', then commit the result." >&2
  exit 1
fi

echo "===========> Generated code is up to date"
