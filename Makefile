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
	DUPLEX_MEDIA_TEST=1 $(GO) test -race -count=1 -timeout 180s -coverprofile=.cache/coverage.out ./...

check:
	@GO="$(GO)" ./scripts/check.sh
