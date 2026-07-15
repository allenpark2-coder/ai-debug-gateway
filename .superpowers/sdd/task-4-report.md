# Task 4 report: atomic diagnostic execution and result waiting

## Status

Implemented atomic policy-gated diagnostic execution and event-driven transaction result waiting.

## RED evidence

- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./internal/gateway -run 'WaitResult|Diagnose' -v`
  - Failed to compile because `Coordinator.WaitResult` and its waiter registry did not exist.
- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./cmd/gatewayd -run Diagnose -v`
  - Failed to compile because the dispatcher had no diagnostic policy, request/result DTOs, or `diagnose.execute` implementation.
- The first transaction-output test then failed because its pre-transaction fixture had not yet reached the asynchronous reader; the test was corrected to establish that precondition before execution.

## GREEN implementation

- Added a bounded, reference-counted waiter registry protected by the coordinator mutex.
- `WaitResult` performs a fast store check and a second check while holding the coordinator lock, preventing missed completion wakeups.
- Every active terminal path uses `finishActiveLocked`, which stores output/result before closing and removing its waiter channel.
- Cancellation removes the caller's registration without stranding other callers; repeated cancellation is covered.
- Daemon stop records `daemon-restarted` for an active transaction and wakes result waiters.
- Approval now preserves the one-active-command rule and rejects secret-active/non-READY execution before consuming a proposal.
- Added transaction-scoped output capture from the transcript sequence recorded at approval.
- Added `diagnose.execute` with a dedicated try-lock mutex, structured policy decisions, ordinary proposal/transaction lifecycle, open-set tracking, audit events, result waiting, output bounding, and truncation flags.
- Reject/edit mutations share the diagnostic mutex so an exposed list result cannot edit the diagnostic proposal between policy evaluation and approval.
- Wired the startup policy snapshot into the dispatcher.

## Verification

- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./internal/gateway ./cmd/gatewayd`
- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...`
- `git diff --check`

All completed with exit status 0. Focused tests cover policy rejection without writes/proposals, accepted marked execution, transaction-only output, concurrent busy rejection, immediate/woken result reads, and cancellation cleanup. Existing coordinator coverage exercises timeout, reboot, disconnect, takeover, and secret/session lifecycle paths through the common terminal recorder.

## Self-review notes

- Result output is capped to 64 KiB for diagnostic responses; output that exceeds the transcript ring is marked start-truncated, and response clipping marks end-truncated.
- The diagnostic busy response currently uses the existing invalid-payload protocol error envelope because the protocol has no dedicated busy code.
