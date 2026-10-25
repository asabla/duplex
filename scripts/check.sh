#!/bin/sh
# Run the same non-rewriting quality gate locally and in CI.
set -eu

GO=${GO:-go}
export GOTOOLCHAIN=go1.27.3
for tool in "$GO" golangci-lint gitleaks govulncheck ffmpeg ffprobe; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf 'Required development tool is missing: %s\n' "$tool" >&2
    exit 1
  }
done
golangci-lint version | awk '$4 == "2.15.0" { found=1 } END { exit !found }'
test "$(gitleaks version)" = 8.30.1
govulncheck -version | awk '/v1.9.0/ { found=1 } END { exit !found }'
mkdir -p .cache
golangci-lint fmt --diff > .cache/format.diff
if test -s .cache/format.diff; then
  cat .cache/format.diff
  printf '%s\n' 'Formatting differs. Run golangci-lint fmt and review the changes.' >&2
  exit 1
fi
golangci-lint run ./...
gitleaks dir --redact=100 --no-banner .
gitleaks git --redact=100 --no-banner .
govulncheck ./...
make test GO="$GO"
awk -f scripts/coverage.awk .cache/coverage.out
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -o .cache/duplex-linux-amd64 ./cmd/duplex
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -o .cache/duplex-linux-arm64 ./cmd/duplex
