#!/usr/bin/env bash
# Fail if any package drops below its baseline in scripts/coverage-baseline.txt.
set -euo pipefail

baseline="$(dirname "$0")/coverage-baseline.txt"
if [[ ! -f "$baseline" ]]; then
  echo "missing $baseline — run scripts/measure-coverage.sh first" >&2
  exit 1
fi

fail=0
while read -r pkg min; do
  [[ -z "$pkg" || "$pkg" =~ ^# ]] && continue
  out=$(go test -race -count=1 -coverprofile=/tmp/harness-cov-check.out "$pkg" 2>&1) || {
    echo "$pkg: tests failed" >&2
    echo "$out" >&2
    fail=1
    continue
  }
  got=$(sed -n 's/.*coverage: \([0-9.]*\)% of statements/\1/p' <<<"$out")
  if [[ -z "$got" ]]; then
    echo "no coverage for $pkg" >&2
    fail=1
    continue
  fi
  awk -v g="$got" -v m="$min" -v p="$pkg" 'BEGIN {
    if (g+0 < m+0) {
      printf "coverage below baseline: %s %.1f%% < %.1f%%\n", p, g, m
      exit 1
    }
  }' || fail=1
done < "$baseline"
exit "$fail"
