# General run state

`internal/state` owns sequential, in-memory state across inference cycles. It
contains runtime facts, task records, knowledge partitions, and an append-only
journal. The existing `loop.State` remains a temporary provider working set.
No storage/recovery engine, domain Go model, MCP infrastructure, or task planner
is introduced. Optional HTTP host work uses the same tasks and event path.

Run the CLI with zero knowledge partitions (the default), or load explicit modules:

```sh
go run ./cmd/grodt --state-definition internal/state/wasm/testdata/state.json "Respond with OK."
```

The JSON definition lists `name`, `schema`, `initial`, `module`, and `abi` for each
partition. File paths resolve relative to the definition file. Module code and
schemas are local resources; no network resolution is introduced. Every default
must validate and every module must compile, instantiate, and pass ABI checks
before initialization succeeds. A bad binary/export/version is a configuration
failure, not a stale partition that the model is expected to repair.

## Ownership and tasks

The host generates run identity, start time, event IDs, task IDs, and counters.
`state.Options` permits a supplied run identity and clock for deterministic tests
and future restoration, but there is no restart/recovery implementation. Snapshots
and journal reads return copies; state modules receive copies of only their own
domain value and an event.

Task records form a tree through canonical `parent_id` relationships. A parent
can have multiple children. `tasks.stack` is only the currently active lineage;
completed children remain in `tasks.records`. `Push` is an explicit host operation,
`Complete` pops the active task and resumes its parent, and `End` terminates the
remaining lineage and pending work. Every proposed task-state document passes the existing JSON
Schema validator before commit. The host alone assigns IDs, status, timing, and
cycle counters.

The CLI creates one root from the run objective and stores the initial request
as its input. Modules may request HTTP work, which the host creates as child tasks;
goal evaluation has no task-planning field. Pending tasks have creation order and
no start time; activation adds them to the active lineage and records start time.
The global 20-cycle limit is unchanged. Structural correction retains its separate
three-attempt limit. Ordinary provider failures still abort; module processing
failures alone do not abort the agent.

## Events and acceptance

The host-created envelope is:

```json
{
  "id": "run-id/event/1",
  "sequence": 1,
  "task_id": "run-id/task/1",
  "timestamp": "2026-09-30T00:00:00Z",
  "source": {"kind": "llm", "id": "run-id/operation/1"},
  "payload": {
    "operation": "goal_evaluation",
    "status": "accepted",
    "result": {"achieved": false, "rationale": "More work is needed."}
  }
}
```

Current source kinds are `user`, `llm`, `runtime`, and `http`. Source identity is supplied
by host adapters, never extracted from model JSON. The CLI admits initial user
input, accepted ordinary generation, and completed goal evaluation. Contract
selection/reduction also admit their completed results when supplied a
`stateflow.Client`.

Structured events are emitted **after** validation correction and downstream
checks finish. Raw client responses and rejected correction candidates are not
domain observations. A terminal operation error emits only an operation label,
error status, and host-authored `operation_failed` code, never candidate data or
provider diagnostics. Cancellation is journaled without starting further module
calls. Inference accounting still counts individual attempts; intermediate
candidates are not copied into the journal.

Events broadcast sequentially in definition order. All modules finish processing
the current event before any resulting host work executes. Commits/failures do not
themselves generate events; completed HTTP operations produce correlated events.
The dispatcher iterates over tasks, with no recursive callback or subscription API.
Only the owning partition is writable. Processing failures do not roll back
successful independent partitions.

`stateflow.Client` injects the complete current intrinsic/task/knowledge snapshot
before every inference, including corrections and all three existing client
methods. Original operation inputs are retained. It does not add old snapshots
to continuation history. Existing temporary guidance remains separate. The CLI
runs inference on the root; HTTP children execute host work without inference or
continuation histories, so they do not consume agent cycles.

## Processing outcomes

| Result | Value | Metadata |
| --- | --- | --- |
| `ignored` | Unchanged | No successful sequence or staleness changes |
| `processed` | Unchanged | Advance processing sequence; clear staleness/failures |
| committed `mutation` | Validated candidate | Advance processing/update sequences and version; clear staleness/failures |
| `error` | Retain last valid value | Set stale; increment failures; retain successful sequences |

**Processed is an explicit module assertion that the event was successfully
incorporated or evaluated and that the partition is again synchronized enough
to clear GRODT staleness. Merely recognizing an event is not sufficient; a module
should return Ignored if the event does not establish that condition.** A committed
mutation carries the same synchronization assertion. There is no `recovers` flag.

Metadata includes `version`, `last_processed_sequence`, `last_update_sequence`,
`last_update_at`, `stale`, `consecutive_failures`, and a single `last_error`.
Both successful sequence fields refer to source event ordering. Initialization is
version zero with successful sequences zero. Domain freshness is represented by
the domain value, independently of GRODT staleness.

Module-reported error, WASM runtime failure, and rejected mutation have distinct
failure kinds. Errors exposed in state are host-selected codes, not guest text,
stack traces, or transport envelopes. A timeout during one module call marks that
partition stale and processing continues for others. Parent-context cancellation
stops remaining delivery; already committed partitions are retained.

## Mutation and journal

Modules return complete replacement or RFC 6902 JSON Patch, never both. Patch
application uses `evanphx/json-patch/v5`, with negative indices, missing-path
removal, and automatic ancestor creation disabled. A patch applies to a private
candidate; the complete result must validate before the partition is committed.
No valid portion of a failed patch is committed. Large JSON integers retain their
original precision.

Paths are rooted at the domain value. A `/metadata` field in a domain value is
merely domain JSON; it cannot access the host's metadata. Modules cannot create
or remove configured partitions. `add` has RFC 6902 semantics (including replacing
an existing object member), not a bespoke create-only operation. Existing contract
`PartialSpecification` still defines optional proposal fields, not patch or merge
semantics. Accepted LLM proposals are observations, never automatic state commits.

Event sequence orders observations. A separate monotonically increasing journal
sequence orders initialization, event receipt, task lifecycle/cycles, processing
outcomes, mutations, and failure metadata. Mutations retain their accepted result
and associated metadata in the journal. Snapshot changes and their entry are
published synchronously without interleaving. Historical failures belong in this
append-only in-memory journal, not in current partition metadata. The journal is
not sent wholesale to the LLM and is not used as a replay-based database.

All store/client operations require sequential use. There is no concurrency,
locking protocol, persistence interface, durability guarantee, or hidden state in
WASM instances. Future persistence must durably pair snapshot transitions with
journal entries; this implementation makes no crash-atomicity claim.

See [the module ABI](state-module-abi.md) for sandbox limits and the reference
fixture. Dynamic module authoring by a SKILL is future work, not part of this
checkpoint.

## Module-requested HTTP work

Enable the capability explicitly with `--allow-state-http` alongside
`--state-definition`, or `state.Options{AllowHTTP: true}` for an embedded host.
WASM retains its import-free ABI and has no direct I/O. The default rejects HTTP
execution with a correlated `http_disabled` result. No LLM credentials or cookies
are injected, and the HTTP transport does not use environment proxies.

Successful `ignored`, `processed`, and `mutation` results can include up to eight
`requests`. Request-only `ignored` makes no synchronization assertion and cannot
clear staleness. A malformed result, conflicting request identity, or rejected
mutation queues no work from that result. A committed mutation survives later
HTTP denial or failure. `error` results cannot request work.

Each accepted request becomes a pending task with its request, requesting
partition, and originating event in `work`. `parent_id` is the causal event's
`task_id`, captured before broadcast. Requests from the same event are siblings,
even when different modules submit them. There is no second queue. Children run
sequentially in creation order; the parent waits. A child stores its HTTP outcome
in `result`, then broadcasts it while still active. Requests produced by that
result become its children and finish before it closes and resumes its parent.
This naturally supports pagination and other multi-step acquisition.

Request IDs match `[A-Za-z0-9_-]{1,64}` and are scoped by partition for the whole
run. Repeating an identical ID/kind/payload reuses the task without another HTTP
execution or result broadcast. Object key order and insignificant whitespace do
not affect payload comparison; JSON number spellings are retained. Conflicting
reuse is an invalid module result. A new logical operation needs a new ID.
Stored outcomes prevent re-execution when the task resumes.

HTTP results use `source.kind: "http"`, `source.id` and `task_id` equal to the host
task ID, and `correlation: {partition, request_id}`. Every module receives the
result through ordinary broadcast. Payload contains `status_code`, bounded
`headers`, base64 `body` bytes, and/or a sanitized `error` code. Empty optional
fields are omitted. Non-2xx responses remain observable HTTP responses; status
codes 400 and above or host errors mark the child failed, without failing its
parent. No HTTP failure directly changes partition values or staleness; modules
choose how to consume the result under the existing validation rules.

| Host resource | Limit |
| --- | --- |
| Requests per module result | `MaxRequestsPerResult = 8` |
| Logical host executions per run | `MaxHostExecutions = 32` |
| Request duration, including redirects and body | `HTTPTimeout = 10s`, or earlier caller deadline |
| Redirects per logical request | `MaxHTTPRedirects = 3` |
| Response body | `MaxHTTPBodyBytes = 1 MiB` |
| Request/response headers | `MaxHTTPHeaderBytes = 64 KiB` |

The host budget is independent of the 20 agent cycles and three structured
attempts: host requests require no inference, but result-driven work chains must
still terminate. Denied/malformed HTTP attempts also consume one host execution,
preventing unbounded failure-driven chains. Redirect hops consume no additional
executions. Exhaustion is the distinct terminal `ErrHostBudget` runtime failure;
remaining tasks fail and prior commits remain. Cancellation stops delivery and
further work; an HTTP outcome already obtained remains recorded on its task.

Initial and redirected URLs pass the same capability/URL checks (HTTP(S), a host,
and no embedded user credentials). Future destination policy must extend this
shared check. Redirect method/body behavior follows Go's HTTP client. No automatic
application retries, allowlists, secret injection framework, persistence, MCP,
or concurrent execution are added in this checkpoint.

Deterministic tests use local HTTP servers and the import-free WASM reference:

```sh
go test ./internal/state/... ./internal/stateflow/... ./cmd/grodt/...
```
