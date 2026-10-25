.DEFAULT_GOAL := help

GO ?= go
export GOTOOLCHAIN = go1.27.3

.PHONY: help build test check

help:
	@printf '%s\n' 'duplex development' '' '  make build  Build bin/duplex (GOOS and GOARCH are optional)' '  make test   Run race tests and controlled media tests' '  make check  Run formatting, lint, tests, coverage, and security checks' '' 'Deployment: docker compose up -d --build'

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/duplex ./cmd/duplex

test:
	@mkdir -p .cache
	@./scripts/coverage-source.sh > .cache/native-coverage-source
	DUPLEX_MEDIA_TEST=1 $(GO) test -race -count=1 -timeout 180s -coverpkg=./... -coverprofile=.cache/coverage-native.out ./...
	@./scripts/coverage-source.sh > .cache/native-coverage-source-end
	@cmp .cache/native-coverage-source .cache/native-coverage-source-end || { printf '%s\n' 'Source changed during tests.' >&2; exit 1; }

check:
	@GO="$(GO)" ./scripts/check.sh
