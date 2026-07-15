.PHONY: verify build test race windows-boundary fmt vet perf clean

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

clean:
	rm -rf dist
