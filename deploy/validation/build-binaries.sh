#!/usr/bin/env bash
# Offline fallback when registries are unavailable. Run in a Linux/amd64
# container with Go 1.26.6 and GCC, read-only source/module mounts, and /out.
set -euo pipefail
validation_output="${1:-/out}"
mkdir -p "$validation_output"
go build -buildvcs=false -trimpath -ldflags='-s -w -X github.com/soulacy/soulacy/internal/config.Version=autopilot-validation' -o "$validation_output/soulacy" ./cmd/soulacy
for validation_package in autopilot gateway runtime channels/mobile llm costs discovery publishedfiles safeundo learning; do
  go test -buildvcs=false -c -trimpath -ldflags='-s -w' -o "$validation_output/${validation_package##*/}.test" "./internal/$validation_package"
done
cp /validation/run.py "$validation_output/run.py"
