GO ?= go
VERSION ?= dev
COMMIT ?= none
BUILD_DATE ?= unknown
LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: build test race vet lint check integration release-snapshot

build:
	mkdir -p build
	CGO_ENABLED=0 GOTOOLCHAIN=local $(GO) build -trimpath -buildvcs=auto -ldflags "$(LDFLAGS)" -o build/mc-router ./cmd/mc-router

test:
	GOTOOLCHAIN=local $(GO) test -count=1 -timeout=90s ./...

race:
	GOTOOLCHAIN=local $(GO) test -race -count=1 -timeout=90s ./...

vet:
	GOTOOLCHAIN=local $(GO) vet ./...
	GOTOOLCHAIN=local $(GO) mod verify

lint:
	test -z "$$(gofmt -l ./cmd ./internal)"
	GOTOOLCHAIN=local $(GO) vet ./...

check: build
	./build/mc-router --check-config --config configs/config.example.toml
	./build/mc-router --check-config --config config.transparent.example.toml
	./build/mc-router --version
	./build/mc-router --help

release-snapshot:
	goreleaser release --snapshot --clean

# First enters fresh network, mount and PID namespaces; never exercises live games.
integration: build
	sudo bash tests/network_integration.sh "$(CURDIR)/build/mc-router"
