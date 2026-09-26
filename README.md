# 0001-grodt

Grodt is an execution runtime between an agent and external tool providers.
The agent proposes; the runtime validates; providers execute; observations feed
back into the next decision. Execution state carries the run forward.

## Current checkpoint: OpenAI-compatible transport

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
MCP integration and trading remain deferred.

## Components and dependency direction

```text
cmd/grodt -> runtime -> agent -> llm
                    -> state
                    -> tools
                    -> observation
                    -> validation -> agent, state, tools
```

- `internal/state`: `AgentState`, `WorldState`, `MemoryEntry`, `Intent`,
  `StatePatch`, `IntentUpdate`, snapshot copying, atomic typed patch application,
  `Store`, and `MemoryStore`. No transport dependencies. The duplicate
  `internal/types` package has been consolidated here.
- `internal/observation`: transient `Observation` and `ErrorInfo`.
- `internal/tools`: `ToolDefinition`, `ToolCall`, `ToolResult`, `Provider`,
  `Registry`, `ProviderRegistry`, JSON Schema validation, and `FakeProvider`.
- `internal/llm`: provider-neutral `Client`, completion types, `FakeClient`,
  `OpenAIClient`, `OpenAIConfig`, environment overrides, and safe `TransportError`s.
- `internal/agent`: `Agent`, `LLMAgent`, `FakeAgent`, `StepInput`, `Decision`, and
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

Each step gives the agent an independent state snapshot and the previous
observation. The LLM prompt includes full state, memory, intents, tool schemas,
and observation errors as data, separate from agent instructions. JSON decoding
rejects unknown fields, trailing values, and invalid decision shapes. Descriptive
`intent` may accompany a tool call; it is never interpreted as an executable action.

Patches cannot express world state or runtime metadata. They are validated
against existing identifiers and applied atomically. Conflicting operations,
duplicate identifiers, unknown removals/updates, and inconsistent intent status
and timestamps are rejected. Memory replacement requires removing the old key
in one step and adding the replacement in a subsequent step.

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

Next checkpoint: implement `mcpclient.Provider`, discover tools dynamically from
one read-only external MCP server, and preserve the existing runtime validation
and provider-neutral interfaces. Do not add trading or MCP server exposure.
