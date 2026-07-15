.PHONY: verify build test race windows-boundary fmt vet perf clean static fuzz-smoke release

GOFLAGS :=

build:
	go build ./...

test:
	go test ./...

race:
	go test -race ./...

# Core session/command/transcript/audit/secret and the common transport
# interface must stay free of any OS-specific import, so a later
# Windows implementation does not require rewriting them.
windows-boundary:
	GOOS=windows GOARCH=amd64 go build ./internal/core/... ./internal/transport

fmt:
	test -z "$$(gofmt -l .)"

vet:
	go vet ./...

verify: build fmt vet race windows-boundary
	go build ./cmd/gateway ./cmd/gatewayd

perf:
	go run ./cmd/gateway-bench -rate 10MiB -duration 30s -max-p99 100ms -runtime-overhead 64MiB

# static is a fast pre-commit check: no build, no tests, just the two
# checks fast enough to run on every save.
static:
	go vet ./...
	test -z "$$(gofmt -l .)"

# fuzz-smoke is a short bounded run, not a full fuzzing campaign: it
# exists to catch a regression before release, not to grow the seed
# corpus. A failure writes its input under testdata/fuzz/<name>/ and
# must be committed as a permanent regression case.
fuzz-smoke:
	go test ./internal/core/command -run '^$$' -fuzz FuzzValidateManaged -fuzztime 30s
	go test ./internal/gateway -run '^$$' -fuzz FuzzMarker -fuzztime 30s

release:
	scripts/build-release.sh $(VERSION)

clean:
	rm -rf dist
