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

## Review follow-up

Review identified that dispatcher-only serialization did not prevent manual coordinator operations or session lifecycle changes from observing/racing the short-lived diagnostic proposal. The follow-up moved the security boundary into `Coordinator.DiagnoseStart`: session/secret/active validation, proposal creation, approval, active installation, and transport write now share one coordinator critical section. All proposal mutation/list APIs also take that lock, so no pending diagnostic proposal is externally visible.

Additional RED/GREEN coverage was added for concurrent manual approval versus diagnostic start, invalid session state without pending residue, immediate transport-write failure, waiter wakeup and exact `disconnected` result, exact ring-capacity truncation metadata, audit ordering, and write-failure open-set cleanup. Write failures now finalize and notify immediately, restore READY via the normal command-result transition, and still return the immutable transaction for consistent result auditing. Auto-readonly approval is audited only after a successful write/start.

The transcript ring's real `Chunk.Gap` is preserved on `command.Result.OutputTruncatedStart`; the dispatcher no longer infers start truncation from output length, eliminating the exact-capacity false positive.

Fresh review verification:

- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./internal/gateway ./cmd/gatewayd` — PASS
- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...` — PASS
- `git diff --check` — PASS

## Final review follow-up

The immediate-write-failure path now records the same approved lifecycle as every other non-nil transaction: `proposal`, `auto-readonly-approval`, `transaction`, then `result`. A nil transaction remains the only pre-approval failure signal. The write-failure dispatcher regression asserts the exact durable audit order.

`finishActiveLocked` now explicitly copies `SourceProposalID` from the immutable active transaction into every terminal `command.Result`; the write-failure/waiter regression asserts this traceability through the common result path.

Fresh verification:

- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test -race ./internal/gateway ./cmd/gatewayd` — PASS
- `GOCACHE=/tmp/ai-debug-gateway-go-cache go test ./...` — PASS
- `git diff --check` — PASS

Inherited minor: transport `Stream.Write` remains synchronous while the coordinator lock is held. Redesigning potentially blocking transport writes is deliberately outside Task 4's review fix scope.
