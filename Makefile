# Thin wrappers. The logic lives in scripts/ so make, hooks and CI run the same thing.
# The binary's version follows its own module tag, cmd/tollgated/vX.Y.Z (ADR 0017),
# exactly as .github/workflows/release.yml stamps it.
VERSION ?= $(shell git describe --tags --match 'cmd/tollgated/v*' --always --dirty 2>/dev/null | sed 's|^cmd/tollgated/||')

.PHONY: build test check fmt lint hooks run clean

build:
	go -C cmd/tollgated build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ../../bin/tollgated .

test:
	scripts/run-checks.sh test

check:
	scripts/run-checks.sh

fmt:
	gofmt -w .

lint:
	scripts/run-checks.sh lint

hooks:
	scripts/install-hooks.sh

run:
	go -C cmd/tollgated run . serve -listen :8080

clean:
	rm -rf bin .logs
