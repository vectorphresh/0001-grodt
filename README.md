# 0001-grodt

Grodt is an execution runtime between an agent and external tool providers.
The agent proposes; the runtime validates; providers execute; observations feed
back into the next decision. Execution state carries the run forward.

## Current checkpoint: 4A — state hygiene

```sh
go run ./cmd/grodt
go test ./...
go test -race ./...
go vet ./...
```

By default, the executable uses a `FakeAgent` and `FakeProvider`. It reads a simulated
account, passes the result into the second decision, terminates on `Done`, and
saves state after both steps. Expected output:

```text
Fake vertical slice completed: 2 decisions, 1 tool call, state version 2
Second decision received observation: {"status":"active"}
```

The default fake demo does not contact a model. To run the live LLM acceptance
scenario with a fake account provider:

```sh
export GRODT_LLM_BASE_URL=http://localhost:8080/v1
export GRODT_LLM_MODEL=your-served-model
export GRODT_LLM_API_KEY=  # Explicitly empty disables auth, including inherited OPENAI_API_KEY.
export GRODT_LLM_REQUEST_TIMEOUT=2m
go run ./cmd/grodt -mode llm
```

The live command verifies two LLM requests, one fake account call, the account
observation in the second request, and a final `Done`. It fails if the model
finishes early or needs more than two requests. This request budget belongs to
the acceptance command; it does not change general runtime loop policy.

| Setting | Environment | Alias | Default |
| --- | --- | --- | --- |
| API root | `GRODT_LLM_BASE_URL` | `OPENAI_BASE_URL` | `http://localhost:8080/v1` |
| Model | `GRODT_LLM_MODEL` | `OPENAI_MODEL` | Required for LLM mode |
| Optional API key | `GRODT_LLM_API_KEY` | `OPENAI_API_KEY` | Empty |
| Request timeout | `GRODT_LLM_REQUEST_TIMEOUT` | — | `2m` |

Precedence: defaults < `OPENAI_*` aliases < `GRODT_LLM_*` variables < explicit
`-base-url`, `-model`, and `-request-timeout` flags. Environment timeout values
must be valid positive Go durations. API keys are supplied through environment
or `llm.OpenAIConfig`, not command-line flags. `.env` files are not loaded
automatically.

Use an API root such as `/v1`; the client appends `/chat/completions`. It sends
nonstreaming POST requests with unchanged system/user messages. Responses are
bounded to 1 MiB, including decompressed bodies. HTTP failures, malformed or
empty completions, and abnormal finish reasons return transport errors. Context
cancellation and request timeouts propagate; there are no automatic retries.
No Authorization header is sent when the key is empty. Errors omit credentials,
URLs, and raw response bodies. Redirects are not followed.

The transport implements the [official Chat Completions contract](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create).
Unknown envelope metadata is allowed, but a single assistant message with
nonempty string content is required. `finish_reason`, when present, must be
`stop`. Native structured-output constraints remain optional future work; strict
JSON decision decoding and runtime validation are still enforced.

The live acceptance run passed against the configured local endpoint. See
[the transport checkpoint report](docs/OPENAI_TRANSPORT_CHECKPOINT.md) for evidence.
The subsequent read-only MCP checkpoint also passed. Trading remains deferred.

## Read-only MCP acceptance

The MCP adapter uses the official Go SDK over **stdio only**. The supplied
configuration starts the published MCP time reference server using `uvx`:

```sh
go run ./cmd/grodt -mode mcp \
  -mcp-config configs/mcp-time.json \
  -mcp-tool get_current_time \
  -goal 'Read the current time in America/Los_Angeles.' \
  -model your-served-model
```

Use the same LLM environment configuration described above. Install `uv` first
if needed. The server package is pinned in `configs/mcp-time.json`; a first run
may download its Python dependencies. Go tests use local subprocess fixtures
and never require that package, Alpaca credentials, or a live model.

The command discovers all remote tools, registers their public names, and
explicitly permits only `time.get_current_time`. It checks ten acceptance criteria
and prints success only after closing the session. A premature `Done` fails.
Repeated tool execution is blocked by the acceptance command. Neither tool
results nor model response text are printed.

MCP configuration fields: `name`, `command`, optional `args`, `dir`,
`inherit_env` (environment variable names), `env` (explicit overrides), and
`startup_timeout` (positive duration, default `30s`). Commands run directly without
a shell. Use a reviewed read-only server/tool; the permission allowlist is explicit
and does not trust a server's read-only annotations as authorization.

Only basic process environment values (PATH, HOME, temporary-directory and locale
settings) are inherited by default. For a credentialed server, explicitly list
its required variable names under `inherit_env`; do not place secrets in command
arguments. Subprocess stderr and SDK logging are discarded. Raw sensitive tool
results and remote error text are not logged.

The current subprocess supervisor requires Unix. It isolates a process group,
closes the SDK session and stdin, waits for the child, and terminates remaining
group members. Lifetime context cancellation also closes the session; repeated
Close calls are safe. No additional MCP transport, retries, production reducers,
or persistence were introduced.

Alpaca paper account acceptance also passed. With `ALPACA_API_KEY` and
`ALPACA_SECRET_KEY` set locally (never committed), run:

```sh
uv sync --frozen --no-dev --project alpaca-mcp-server
go run ./cmd/grodt -mode mcp \
  -mcp-config configs/mcp-alpaca-paper.json \
  -mcp-tool get_account_info \
  -goal 'Read the Alpaca paper account status once. Do not modify the account, create orders, or call any other tool.' \
  -model your-served-model
```

This configuration forces paper mode and selects the server's account toolset.
It discovers 11 tools, including documentation and account-update capabilities,
but permits **only `alpaca.get_account_info`**. The account observation is sent
to the configured LLM; neither account data nor credentials are printed.
The server's security envelope and advertised schemas are preserved. The
successful Alpaca run executed the selected tool once and passed all ten checks.

See [MCP_CHECKPOINT.md](docs/MCP_CHECKPOINT.md) for the ten live PASS results,
schema mapping, normalization, error classification, and remaining limits.

## Components and dependency direction

```text
cmd/grodt -> runtime -> agent -> llm
                    -> state
                    -> tools
                    -> observation
                    -> validation -> agent, state, tools
cmd/grodt -> mcpclient -> tools, official MCP SDK
```

- `internal/state`: `AgentState`, `WorldState`, `MemoryEntry`, `Intent`,
  `StatePatch`, `IntentUpdate`, snapshot copying, atomic typed patch application,
  `Store`, and `MemoryStore`. No transport dependencies. The duplicate
  `internal/types` package has been consolidated here.
- `internal/observation`: transient `Observation` and `ErrorInfo`.
- `internal/mcpclient`: stdio `Provider`, dynamic discovery, generic result
  normalization, configuration, sanitized errors, and session/process cleanup.
- `internal/tools`: `ToolDefinition`, `ToolCall`, `ToolResult`, `Provider`,
  `Registry`, `ProviderRegistry`, JSON Schema validation, and `FakeProvider`.
- `internal/llm`: provider-neutral `Client`, completion types, `FakeClient`,
  `OpenAIClient`, `OpenAIConfig`, environment overrides, and safe `TransportError`s.
- `internal/agent`: `RunSpec`, `Agent`, `LLMAgent`, `FakeAgent`, `StepInput`, `Decision`, and
  recoverable `InvalidDecisionError`. Agent semantics remain independent of the
  HTTP transport behind `llm.Client`.
- `internal/validation`: decision, state-patch, permission, and composable action
  validators. Domain-specific risk rules can be supplied through `Action`.
- `internal/runtime`: `Runtime.Step`, thin `Runtime.Run`, `StepResult`, optional
  trusted `Reducer`s, and an injectable clock for deterministic tests.

## Execution rules

Provider registration performs discovery once, validates supplied schemas, and
atomically registers namespaced tools. `fake.get_account` routes to the remote
name `get_account`. Duplicate providers/tools are rejected, and tool listings
are sorted. Schema validation uses `github.com/santhosh-tekuri/jsonschema/v6`;
schemas must be self-contained (local references work; external references are
rejected without filesystem or network access).

`validation.NewValidators()` denies all tools. Pass exact public tool names to
allow them, for example `validation.NewValidators("fake.get_account")`.
Additional action/risk policies run after schema and permission checks and
before the provider. No trading-specific risk policy is implemented yet.

Each runtime is constructed with an explicit `agent.RunSpec{Goal, Instructions}`.
It owns a value copy for the entire run. Each step gives the agent that fixed
specification, an independent state snapshot, and the latest observation.
`AgentState` has no goal field: task definition and mutable progress are separate.
The LLM prompt includes the specification, full state, memory, intents, tool
schemas, and observation errors, with decision instructions in a system message. JSON decoding
rejects unknown fields, trailing values, and invalid decision shapes. Descriptive
`intent` may accompany a tool call; it is never interpreted as an executable action.

Patches cannot express the run specification, world state, or runtime metadata. They are validated
against existing identifiers and applied atomically. Conflicting operations,
duplicate identifiers, unknown removals/updates, and inconsistent intent status
and timestamps are rejected. `upsert_memory` replaces an entire entry at its stable
`key`, including its timestamp, or creates it if absent. `remove_intent` explicitly
retires an existing intent; no external action is executed or cancelled by removal.
There is no automatic intent retirement or permanent intent history.

Working memory is limited to **64 entries**, each at most **4,096 bytes** when
encoded with Go's `encoding/json.Marshal` (key, value, timestamp, and JSON
escaping included). A patch exceeding either limit is rejected in full. Removing
one key can make room for another in the same patch, but operations on the same
key/ID cannot be combined. No automatic eviction occurs. Loaded/saved memory is
also validated outside the model. There is no expiration field or TTL policy.

These are working-memory bounds, not a total prompt budget: world snapshots,
intent counts/content, tool definitions, and observations have no new global cap.
Callers and agents must maintain relevant intents explicitly.

Successful tool output must be valid JSON and satisfy the output schema when
provided. Only then can trusted reducers consume it. Observations never become
working memory automatically. Reducers are supported and tested, but no domain
reducer is needed by the fake executable.

Recoverable decision, patch, schema, permission, action, and provider errors
become observations. Validation failures roll back the proposed patch. After a
provider attempt, a valid patch is retained even if the provider fails or its
output is invalid. No failure automatically retries a tool call.

Every completed step, including `Done` and recoverable failure steps, advances
the state version once and sets `UpdatedAt`. Storage, discovery/listing, LLM
transport, reducer, cancellation, and explicitly marked `tools.FatalError`
failures stop the run. Observation feedback is installed only after a successful
save; a step with no new observation clears the prior observation.

## Verification and limits

Tests cover the two-step fake vertical slice, state snapshot isolation and
patch atomicity, context construction and strict decoding, registry discovery
and routing, duplicate detection, schemas, permissions and action rejection,
error recovery, trusted reduction, versioning, cancellation, and fatal failures.
`FakeAgent.Inputs`, `FakeClient.Requests`, and `FakeProvider.Calls` make boundary
behavior inspectable in tests.

The store is in memory and does not survive process exit. One active runtime
writer should own a store; it does not provide cross-runtime compare-and-swap.
The test fakes are intended for sequential use. Tool execution and state saving
are not a transaction: if saving or reduction fails after a tool has executed,
execution stops and the caller must reconcile the external outcome before
retrying. Automatic retry, durable execution journals, loop limits, structured
run logging, and lifecycle controls are deferred.

The transport suite also covers request path/model/message encoding, optional
auth, malformed JSON/envelopes, empty completions, non-2xx statuses, redirects,
response size boundaries, cancellation before headers and during body reads,
timeouts, and configuration precedence. Command tests verify all seven acceptance
criteria through a local HTTP server and reject premature completion.

MCP tests additionally exercise discovery pagination, namespacing and remote-name
routing, schema preservation/absence, collisions, unchanged argument values,
result normalization, recoverable and fatal errors, cancellation notifications,
startup timeout, subprocess cleanup, and the complete HTTP-to-stdio acceptance
flow. No external service is required for `go test ./...`.

Checkpoint 4A is complete: immutable run specification, atomic memory upserts,
explicit intent retirement, deterministic memory bounds, and a 150-step synthetic
state/prompt regression test. See [STATE_HYGIENE_CHECKPOINT.md](docs/STATE_HYGIENE_CHECKPOINT.md)
for policies, compatibility changes, acceptance evidence, and verification.
Checkpoint 3's live time and Alpaca results remain historical evidence; no live
provider or model call is needed for state-hygiene verification. Next,
separately specify trusted account reduction with field-level validation of the
preserved security envelope. Trading, MCP server exposure, and durable storage
remain outside this checkpoint.
