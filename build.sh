#!/usr/bin/env bash
# Cross-compile ocireg for the WD My Cloud EX2 Ultra (32-bit ARMv7, Linux, static).
set -euo pipefail
cd "$(dirname "$0")"
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="-s -w" -o ocireg-armv7 .
file ocireg-armv7 2>/dev/null || ls -l ocireg-armv7
