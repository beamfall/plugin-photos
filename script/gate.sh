#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

run_gate() {
  echo "== gofmt =="
  fmt_out="$(gofmt -l .)"
  if [ -n "$fmt_out" ]; then
    echo "gofmt: files need formatting:"
    echo "$fmt_out"
    exit 1
  fi

  echo "== go vet =="
  go vet ./...

  echo "== test-depth contract + per-package coverage =="
  python3 .github/workflows/test_depth_gate.py check

  echo "== golangci-lint =="
  golangci-lint run ./...

  echo "ALL GATES GREEN"
}

case "${1:-gate}" in
  gate)
    run_gate
    ;;
  race)
    go test -race -count=1 ./...
    ;;
  fuzz)
    fuzz_time="${FUZZ_TIME:-20s}"
    if [[ ! "$fuzz_time" =~ ^([1-9][0-9]*)(s|x)$ ]]; then
      echo "fuzz: FUZZ_TIME must be a positive whole-second or execution budget" >&2
      exit 1
    fi
    python3 .github/workflows/test_depth_gate.py contract
    go test -run '^$' -fuzz '^FuzzReadEXIF$' -fuzztime="$fuzz_time" ./internal/exif
    ;;
  *)
    echo "usage: $0 [gate|race|fuzz]" >&2
    exit 2
    ;;
esac
