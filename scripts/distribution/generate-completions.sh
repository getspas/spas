#!/usr/bin/env bash
set -euo pipefail

# Build for the release host so cross-compiled artifacts never need to run.
output=.distribution/completions
mkdir -p "$output"
binary=$(mktemp "${TMPDIR:-/tmp}/spas-completions.XXXXXX")
trap 'rm -f "$binary"' EXIT
CGO_ENABLED=0 go build -trimpath -o "$binary" .
"$binary" completion bash > "$output/spas.bash"
"$binary" completion zsh > "$output/_spas"
"$binary" completion fish > "$output/spas.fish"
"$binary" completion powershell > "$output/spas.ps1"
