# grodt.state/v1 module ABI

The current ABI uses import-free core WebAssembly executed by wazero. It does not
require WASI, WIT/component bindings, libc, or a GRODT-specific SDK. Guest modules
have no host filesystem, network, clock, random, credentials, or operation
callbacks. Time and observations are explicit inputs.

## Exports and ownership

Required exports:

```text
memory                                  32-bit linear memory
 grodt_state_abi_version() -> i32         must return 1
 grodt_alloc(length: i32) -> i32          input buffer offset
 grodt_process(pointer: i32, length: i32) -> i64
```

The process result packs the unsigned output offset in the high 32 bits and
unsigned output length in the low 32 bits: `(uint64(offset) << 32) | length`.
Offsets refer to exported `memory`. The input and output are UTF-8 JSON, with no
trailing NUL included in their lengths. Pointers must name valid memory ranges.
The host checks the output size before reading/copying it. Allocation must return
space for the entire requested input; out-of-bounds offsets fail processing.

The host compiles code once and creates a fresh instance for every call, checks
its ABI version, allocates/writes input, invokes processing, copies output, and
closes the instance. No free export is needed. Hidden memory is never authoritative
and cannot accumulate across calls. Initialization also instantiates and checks
the version before a run can start; there is no domain initialization callback.
A core WASM start section executes within the same limits; conventional exported
`_start` functions are not automatically invoked.

## Input

```json
{
  "abi": "grodt.state/v1",
  "current": 0,
  "event": {
    "id": "example/event/1",
    "sequence": 1,
    "timestamp": "2026-09-30T00:00:00Z",
    "source": {"kind": "runtime", "id": "example"},
    "payload": {"action": "increment"}
  }
}
```

`current` is only this module's partition value. The envelope is host-owned;
`payload` is opaque observation data. Equivalent state/event input should produce
equivalent output. No host clock or entropy is available, but modules still bear
responsibility for deterministic algorithms. Deadlines are operational limits,
not deterministic fuel accounting.

## Results

The existing outcome shapes remain supported:

```json
{"status":"ignored"}
{"status":"processed"}
{"status":"mutation","replace":1}
{"status":"mutation","patch":[{"op":"add","path":"/item","value":1}]}
{"status":"error"}
```

Unknown fields, duplicate result fields, conflicting mutation forms, unknown
statuses, and extra JSON documents are rejected. `replace:null` is a valid
replacement only if the partition schema permits null. Patch is an RFC 6902 array,
including its standard add/remove/replace/move/copy/test operations. A result cannot
address another partition, host metadata, or task state.

`Processed` explicitly asserts successful incorporation/evaluation **and** that
knowledge is synchronized enough to clear GRODT staleness. Recognition alone is
not enough; return `Ignored` if the event does not establish that condition.
A committed `Mutation` carries the same synchronization assertion. Return `Error`
for a relevant event that could not be processed. The host retains the old value
and publishes a generic module-error classification. Guest error strings are not
part of v1; no arbitrary diagnostic text is exposed to the LLM.

`Processed` advances `last_processed_sequence`; a committed mutation advances
both successful sequence fields. Both clear stale/error metadata. `Ignored` does
neither. Rejected mutations and runtime failures retain the last valid value and
mark it stale. No module failure/commit is automatically broadcast again.

## Host request extension

The v1 exports, memory ownership, and fresh-instance lifecycle are unchanged.
Optional `progress` may accompany `processed` or `mutation`. It replaces the
owning partition's entire progress list atomically with a successful result;
omission retains the list, and `[]` clears it. It is rejected on `ignored` or
`error`. Existing results remain compatible.

Each record has `id`, `task_id`, `kind` (`conclusion`, `completed_step`, `focus`),
`status` (`established`, `unresolved`, `invalidated`), and an outcome `summary`.
Optional `fact_reference` is a JSON pointer into the owning partition value.
Facts and fixed references remain in that value; records describe established
outcomes, never private reasoning, prior responses, or tool result copies.
`task_id` must reference an existing task. Lists contain at most 32 records with
unique IDs; summaries are at most 512 bytes, task IDs and references 256 bytes.
The host stamps `knowledge_version`; modules must omit it or supply zero.

Model projection includes only records scoped to active tasks. Records appear
under `established_progress` or, for focus records, `active_focus`. Stale partition
metadata or a changed partition version projects their status as `invalidated`
without altering the audit record. Modules reassert still-valid outcomes when
updating their value; the runtime interprets no domain semantics. Progress is
journaled with the accepted module result and retained in full snapshots.

Optional `requests` may accompany `ignored`, `processed`, or `mutation`, never
`error`. Older modules remain compatible; older hosts reject the new field rather
than silently dropping work. For example:

```json
{
  "status": "ignored",
  "requests": [{
    "id": "page-1",
    "kind": "http",
    "payload": {
      "method": "GET",
      "url": "https://example.org/data?page=1",
      "headers": {"Accept": "application/json"}
    }
  }]
}
```

The generic envelope contains `id`, `kind`, and opaque JSON `payload`; only `http`
is supported. HTTP payload requires `method` and `url`, with optional string-valued
`headers` and JSON `body`. A supplied body is transmitted as JSON bytes; the host
sets `Content-Type: application/json` when absent. Requests require an active
causal task. A result may supply at most eight requests with distinct IDs.

A request does not itself change domain state or imply synchronization. In
particular, request-only `ignored` leaves successful sequences and staleness
unchanged. Invalid result/mutation submissions schedule no work. Work tasks retain
the request and provenance, and execute only after complete event broadcast.
HTTP failure cannot undo an already validated mutation.

The response is an ordinary event, broadcast to every partition:

```json
{
  "id": "example/event/2",
  "sequence": 2,
  "timestamp": "2026-09-30T00:00:01Z",
  "task_id": "example/task/2",
  "source": {"kind": "http", "id": "example/task/2"},
  "correlation": {"partition": "catalog", "request_id": "page-1"},
  "payload": {
    "status_code": 200,
    "headers": {"Content-Type": ["application/json"]},
    "body": "e30="
  }
}
```

The body is base64 of raw bytes (here `{}`), not assumed to be JSON or UTF-8.
Failures expose a sanitized host error, e.g. `{"error":"http_disabled"}`.
Modules inspect correlation and choose their existing outcome; irrelevant results
can be ignored. A result may request the next page with a fresh ID. Correlation is
host metadata and cannot be set by a module. `task_id` is omitted for synthetic
host events admitted without an active task.

HTTP is disabled unless the host explicitly enables it. Each logical operation
allows three redirects under the same capability/URL checks, a ten-second timeout,
a 1 MiB response body, and 64 KiB headers. The run permits 32 host executions,
independently of inference cycles; redirects do not increment that count.
See [general state](general-state.md#module-requested-http-work) for task ordering,
idempotency, capability boundaries, and failure semantics.

## Execution limits

| Resource | Limit |
| --- | --- |
| WASM binary | 8 MiB |
| Guest linear memory | 1,024 pages / 64 MiB |
| Serialized combined input | 8 MiB |
| Module output | 4 MiB |
| Complete partition value | 4 MiB |
| Patch operations | 256 |
| Instantiation/version/allocation/processing per call | 1 second, or earlier caller deadline |

The host uses `WithCloseOnContextDone(true)` and closes instances after every call.
Excessive `memory.grow` fails inside WASM; a guest may handle that failure normally.
Out-of-range output, excessive output, traps, and malformed results never commit.
Patch copy growth is limited in the patch library as well as final document size.
Limits do not cap total host-process memory or guarantee a compilation deadline.
Definitions/modules are explicitly configured local artifacts, not runtime-loaded
model-generated binaries. Stronger whole-process isolation is not implemented.

## Minimal reference fixture

`internal/state/wasm/testdata/reference.c` is a small freestanding C fixture for an
integer partition and synthetic `action` events: `increment`, `processed`, `error`,
`request`, or other/ignored. The `request` action emits one HTTP request to
`http://example.invalid/`; the ABI integration test leaves HTTP disabled. It has no external imports. Its fixed-input scanning is specific
to this test protocol, not a general JSON parser to copy into production modules.
Its internal call counter intentionally exposes incorrect instance reuse.

The checked-in `.wasm` files let `go test ./...` run without a C/WASM compiler.
Rebuild with Clang and a WASM linker, for example the installed Clang 14/linker 15:

```sh
clang --target=wasm32 -O2 -nostdlib -fno-builtin \
  -fuse-ld=/usr/bin/wasm-ld-15 -Wl,--no-entry -Wl,--max-memory=67108864 \
  internal/state/wasm/testdata/reference.c \
  -o internal/state/wasm/testdata/reference.wasm

for mode in 1 2 3 4 5 6 7 8 9; do
  clang --target=wasm32 -O2 -nostdlib -fno-builtin \
    -fuse-ld=/usr/bin/wasm-ld-15 -Wl,--no-entry -Wl,--allow-undefined \
    -Wl,--max-memory=67108864 -DTEST_MODE=$mode \
    internal/state/wasm/testdata/reference.c \
    -o internal/state/wasm/testdata/fault-$mode.wasm
done
```

Fault modes cover wrong version, trap, infinite execution, invalid output offset,
oversized output, malformed JSON, a prohibited import, bounded memory growth,
and an invalid result envelope. `state.json`, `schema.json`, and `initial.json`
form a runnable reference definition. This is one reference/test module, not a
multi-language SDK or dynamic authoring toolchain.

## Generic MCP tool state module

`modules/mcp-tool-state/state.json` loads a generic latest-result partition using
this unchanged v1 ABI. MCP events use `source: {"kind":"mcp","id":"<server name>"}`
and `payload: {"tool":"<original name>","result":<accepted MCP result envelope>}`.
The ordinary `task_id` and correlation retain invocation provenance. See the
[module documentation](../modules/mcp-tool-state/README.md) for state shape,
resource bounds, and the unresolved distinction between partition staleness and
entry freshness.
