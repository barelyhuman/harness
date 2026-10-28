#!/usr/bin/env bash
# Measure statement coverage per Go package (one line per package: "<import path> <min percent>").
set -euo pipefail

baseline="${1:-$(dirname "$0")/coverage-baseline.txt}"
: > "$baseline"
while read -r pkg; do
  out=$(go test -race -count=1 -coverprofile=/tmp/harness-cov.out "$pkg" 2>&1) || {
    echo "$pkg FAILED: $out" >&2
    exit 1
  }
  pct=$(sed -n 's/.*coverage: \([0-9.]*\)% of statements/\1/p' <<<"$out")
  echo "$pkg $pct" >> "$baseline"
  echo "$pkg $pct%"
done < <(go list ./... | grep -v '^github.com/sid/harness$')
