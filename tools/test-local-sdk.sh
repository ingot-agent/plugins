#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    printf 'Usage: %s <sdk-checkout>\n' "$0" >&2
    exit 2
fi

if [[ ! -f "$1/go.mod" ]]; then
    printf 'SDK checkout must contain go.mod: %s\n' "$1" >&2
    exit 2
fi

sdk_dir=$(cd -- "$1" && pwd -P)
plugins_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
sdk_module=$(cd -- "$sdk_dir" && GOWORK=off go list -m)
if [[ "$sdk_module" != github.com/ingot-agent/sdk ]]; then
    printf 'Expected github.com/ingot-agent/sdk, found %s\n' "$sdk_module" >&2
    exit 2
fi

modules=(
    "$sdk_dir"
    "$plugins_dir/model-openai-compatible"
    "$plugins_dir/model-openai-responses"
    "$plugins_dir/model-runtime"
    "$plugins_dir/agent-default"
    "$plugins_dir/context-compact"
    "$plugins_dir/usage-default"
)

validation_dir=$(mktemp -d "${TMPDIR:-/tmp}/ingot-local-sdk.XXXXXXXX")
trap 'rm -f -- "$validation_dir/go.work" "$validation_dir/go.work.sum"; rmdir -- "$validation_dir"' EXIT
(
    cd -- "$validation_dir"
    GOWORK=off go work init "${modules[@]}"
)
export GOWORK="$validation_dir/go.work"

for module_dir in "${modules[@]}"; do
    printf 'Checking %s\n' "$module_dir"
    (
        cd -- "$module_dir"
        go vet ./...
        go test -race ./...
    )
done
