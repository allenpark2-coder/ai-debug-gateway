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

# gateway-bench's defaults already are the release gate: 10 MiB/s of
# synthetic target output for 30s, failing if human input latency's
# p99 reaches 100ms.
perf:
	go run ./cmd/gateway-bench

clean:
	rm -rf dist
