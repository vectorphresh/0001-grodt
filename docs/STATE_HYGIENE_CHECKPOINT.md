# Checkpoint 4A — State hygiene and SKILL.state alignment

September 26, 2026. **Complete: automated acceptance and verification passed.**

## Scope

Execution context is reconstructed from a fixed run specification, current
structured state, the latest observation, and discovered tool definitions.
Previous decisions, reasoning summaries, and observations are not replayed.
This checkpoint improves state maintenance without adding skills, trading,
production reducers, durable storage, or new dependencies.

## Run specification and ownership

`agent.RunSpec` has explicit `Goal` and optional `Instructions` strings.
`runtime.NewRuntime(spec, store, agent, registry, validators, options...)` takes
it by value and retains an unexported copy. Every `StepInput` contains `Spec`
separately from `State`. Changing the caller's or agent's copy cannot change the
runtime's specification. There is no run-spec setter or patch operation.

`AgentState.Goal`, `StatePatch.Goal`, and `StepInput.Goal` have been removed.
Strict decision decoding rejects attempts to patch `goal`, `spec`, or
`instructions`, just as it rejects `world`, `version`, and `updated_at`.
World state remains owned by trusted reducers/runtime code. State snapshot
isolation, runtime revisions, and observation feedback semantics are preserved.

The system prompt describes the allowed decision/patch fields and tells the
model to follow the runtime-owned specification. Mutable state and observations
remain data. Native endpoint structured-output support was not introduced.

## Patch semantics

Memory continues to use `MemoryEntry.Key` as its stable identifier; there is no
field rename or arbitrary JSON Patch format.

| Operation | Behavior |
| --- | --- |
| `add_memory` | Create an entry with an absent key; an existing key is an error. |
| `upsert_memory` | Replace the complete entry at a key, including `ObservedAt`, or append a new entry when absent. Existing entry order is preserved. |
| `remove_memory` | Remove an existing key. |
| `add_intent` | Create an intent with an absent ID. |
| `update_intent` | Update execution status/time for an existing ID. |
| `remove_intent` | Explicitly remove an existing ID from active state. |

Every memory key and every intent ID can appear in at most one operation within
a patch. Duplicate operations and all cross-operation conflicts are rejected,
including add/upsert, upsert/remove, and update/remove. Unknown removals and
intent updates fail. An invalid operation rejects the entire patch before any
mutation, including otherwise valid changes in that patch.

Intent execution status remains the existing `Executed`/`ExecutedAt` pair.
There is no new status enum, event history, or automatic garbage collection.
Explicit removal is allowed for active or completed intents, enabling callers
to retire cancelled or irrelevant plans without claiming they executed.
Removing an intent does not execute or cancel an external provider action.

## Memory policy

- `MaxWorkingMemoryEntries = 64`.
- `MaxMemoryEntryBytes = 4096`.
- Entry size is the byte length of `encoding/json.Marshal(MemoryEntry)`, including
  key, value, timestamp, JSON syntax, and escaping. It is not a character count
  or token estimate.
- Keys and values must be nonblank; keys must be unique.
- Bounds apply to the resulting memory after all proposed operations. Removing
  one key can make room for another in the same patch. There is no eviction.
- Patch validation, `MemoryStore.Save`, and runtime load/save boundaries enforce
  the policy outside the model. Invalid loaded memory fails before an agent call.
- Agent-proposed violations become recoverable `invalid_state_patch` observations;
  no proposed state changes or accompanying tool calls are applied. Runtime
  revision/timestamp updates for recoverable failures remain as before.

There is no `ExpiresAt` field in the existing model, so none was added and no
expiration cleanup runs. `ObservedAt` is metadata, not an expiry deadline.

These limits bound working memory, not the entire execution context. Intent
counts/content, trusted world snapshots, specifications, tools, and observations
have no new global cap. Explicit intent retirement is supported and exercised;
no universal constant-size prompt guarantee is claimed for arbitrary workloads.

## Test evidence

New state tests cover creating/updating memory by upsert, complete-entry
replacement, snapshot isolation, explicit intent removal, every requested
conflict, and atomic rollback across mixed memory/intent operations. Boundaries
include exactly-at and above-limit sizes/counts, oversized upserts, large keys,
UTF-8 and JSON escaping, and removals that do or do not make enough room.

A multi-step `FakeAgent` test reads an initial earnings fact, stores it, reads a
correction, replaces the same key atomically, then removes it. Subsequent
model-visible state contains one corrected entry and then none. A separate
`FakeAgent` test adds, completes, and explicitly retires an intent.

The long-run test performs **150 synthetic tool/patch steps plus Done**, using
`LLMAgent` with a deterministic `FakeClient` and `FakeProvider`. It exercises
actual prompt construction, repeatedly upserts one fact, and completes/removes
50 intents. Every request is checked for:

- the unchanged run specification and tool definition;
- exactly two messages, with no previous decision summaries;
- only the latest observation and current conclusion;
- at most one relevant intent, with removed intents absent;
- compliance with the working-memory count/entry policy and a fixed byte budget
  for this fixed-size workload, allowing revision/observation-ID digit growth.

Recording fakes retain test evidence only; their recorded history never becomes
model input. No tokenizer or real model is needed.

Existing CLI transport tests now also check that the intended `RunSpec` reaches
each HTTP request. The pre-existing suite continues to cover fake execution,
OpenAI transport, MCP discovery/routing, local HTTP-to-stdio acceptance,
permissions, reducer ownership, cancellation, and cleanup. No live time-server,
Alpaca API, or model run was repeated for this checkpoint; the prior live evidence
remains in `MCP_CHECKPOINT.md`.

## Verification and acceptance

All required checks passed:

```text
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

| Criterion | Result |
| --- | --- |
| 1. Specification separate from mutable state | PASS |
| 2. LLM cannot patch specification | PASS |
| 3. Atomic correction at a stable memory key | PASS |
| 4. Intent completion and explicit removal | PASS |
| 5. Deterministic memory count/size limits | PASS |
| 6. Atomic rejection of limit violations | PASS |
| 7. Expiration when already supported | Not applicable: no expiration field exists; none added. |
| 8. Multi-step stale-fact replacement test | PASS |
| 9. Synthetic long-run current-context test | PASS: 150 tool/patch steps plus Done. |
| 10. Pre-existing automated regression/integration tests | PASS: complete regular and race suites; live services not rerun. |

## Changed files

| Files | Change |
| --- | --- |
| `internal/agent/spec.go` (new) | Explicit runtime-owned `RunSpec`. |
| `internal/agent/agent.go`, `agent_test.go` | Separate spec/progress context, updated patch instructions and strict decoding coverage. |
| `internal/state/state.go`, `patch.go` | Remove mutable goal; add atomic upserts and intent removal. |
| `internal/state/memory.go` (new), `memory_store.go` | Deterministic memory policy and snapshot validation. |
| `internal/state/state_test.go`, `hygiene_test.go` (new) | API migration, bounds, conflicts, atomicity, isolation. |
| `internal/runtime/runtime.go`, `runtime_test.go` | Own spec, validate memory boundaries, migrate fixtures. |
| `internal/runtime/hygiene_test.go` (new) | Stale facts, intent lifecycle, spec isolation, invalid-state recovery, long-run verification. |
| `cmd/grodt/main.go`, `mcp.go`, `main_test.go`, `mcp_test.go` | Supply explicit specs and test CLI propagation. |
| `internal/mcpclient/client_test.go` | Constructor signature migration only; adapter implementation unchanged. |
| `README.md`, this report | Current policy, compatibility notes, checkpoint evidence. |

## Compatibility and stop point

This is an intentional internal API/data-shape change: runtime constructors need
an explicit spec, and consumers of `StepInput` must use `spec.goal` rather than
`goal` or `state.goal`. Old `state_patch.goal` responses now produce validation
feedback. Memory key names and all previous non-goal patch fields remain intact.
All repository callers have been migrated. There is no durable state format to
migrate, and command-line flags/environment configuration remain unchanged.

The MCP adapter implementation, OpenAI transport, provider configuration,
permissions, and reducer behavior were not changed. No live credentials were
used. Existing unrelated untracked files were left alone.

Stop after this checkpoint. The next proposed milestone remains a separately
specified minimal trusted Alpaca account reducer with field-level validation
of the preserved envelope. That work has not started here.
