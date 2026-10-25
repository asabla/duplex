GO ?= go
export GOTOOLCHAIN = go1.27.3

.PHONY: build test media-test vet linux verify

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/duplex ./cmd/duplex

test:
	$(GO) test -race -count=1 ./...

media-test:
	DUPLEX_MEDIA_TEST=1 $(GO) test -race -count=1 -timeout 120s -v ./internal/media

vet:
	$(GO) vet ./...

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o bin/duplex-linux-arm64 ./cmd/duplex
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o bin/duplex-linux-amd64 ./cmd/duplex

verify: test vet build linux
