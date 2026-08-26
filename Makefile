# Convenience targets. Everything here is a thin wrapper around a command you
# can also run directly; nothing is hidden behind make.

GO ?= go
BIN ?= cloudcc

.PHONY: build test fmt vet check golden sdk-test e2e e2e-deploy fuzz diff load doctor clean

build:              ## Build the cloudcc binary
	$(GO) build -o $(BIN) ./cmd/cloudcc

fmt:                ## Format Go sources
	gofmt -w ./cmd ./internal

vet:
	$(GO) vet ./...

test:               ## Unit and golden tests (no network)
	$(GO) test ./... -count=1

golden:             ## Accept the current output as the golden trees
	$(GO) test ./internal/cli -update
	@echo "review the diff before committing: git diff internal/cli/testdata"

sdk-test:           ## Python SDK and shim-parity suite
	cd sdk/python && uv run --with pytest --with-editable . python -m pytest tests -q

check: fmt vet test sdk-test  ## Everything that needs no network

e2e:                ## Provisioning and functional tests against the emulator
	./tests/e2e/ministack.sh

e2e-deploy:         ## The same path through `cloudcc deploy`
	./tests/e2e/deploy.sh

fuzz:               ## Sweep generated programs looking for compiler bugs
	CLOUDCC_FUZZ_SEEDS=$${SEEDS:-200} $(GO) test ./internal/fuzz -run TestSweep -timeout 30m

diff:               ## Differential test: uncompiled vs compiled behaviour
	./tests/e2e/differential.sh

load:               ## Load test: throughput before/after, and every edge checked
	./tests/e2e/load.sh

doctor: build       ## Check the local toolchain
	./$(BIN) doctor

clean:
	rm -rf $(BIN) compiled
