# Thin wrappers. The logic lives in scripts/ so make, hooks and CI run the same thing.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)

.PHONY: build test check fmt lint hooks run clean

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/tollgated ./cmd/tollgated

test:
	go test -race ./...

check:
	scripts/run-checks.sh

fmt:
	gofmt -w .

lint:
	scripts/run-checks.sh lint

hooks:
	scripts/install-hooks.sh

run:
	go run ./cmd/tollgated serve -listen :8080

clean:
	rm -rf bin .logs
