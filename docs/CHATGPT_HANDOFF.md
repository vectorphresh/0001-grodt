# 0001-grodt — Implementation checkpoint and ChatGPT handoff

Prepared: September 25, 2026

Historical first-milestone report. The subsequent OpenAI transport milestone is
complete; see [the updated checkpoint](OPENAI_TRANSPORT_CHECKPOINT.md).

This report summarizes a review and repair of the implementation against the
“0001-grodt — Loop Middleware Architecture Plan” created in ChatGPT. It is
self-contained context for continuing that design conversation.

**Current status: the first fake vertical slice is implemented and tested.**
The complete real-LLM-plus-MCP definition of done has not been reached.

## 1. Objective and architectural boundaries

Grodt is an autonomous execution middleware runtime between an LLM and external
capability providers. The intended relationship remains:

```text
LLM proposes
    -> Grodt validates
    -> Provider performs
    -> Observation reports the outcome
    -> Trusted reducer establishes world state
    -> State preserves information needed for subsequent decisions
```

MCP will be an external adapter implementing `tools.Provider`. It is not the
internal runtime architecture. A future MCP server would be a separate control
adapter, and is not part of this checkpoint.

Execution context is rebuilt from state and the latest observation. Neither
conversation history nor a permanent log of all tool responses serves as the
execution memory.

## 2. Initial review findings

The Copilot implementation compiled but was an incomplete scaffold:

- Every runtime step created a dummy observation instead of receiving the prior
  tool result, breaking the feedback loop.
- Input/output schema checks and permissions were skipped; patch validation
  always succeeded.
- The memory store retained mutable aliases, permitting stored data to change
  without a successful save.
- Prompts omitted world state, memory/intent contents, tool schemas, and error
  details.
- Recoverable validation and provider errors terminated the loop.
- Registry namespacing, collision detection, and remote-name mapping were absent.
- The prompt allowed an `intent` with a tool call, but the validator rejected it.
- There were no automated tests. `go test ./...` reported no test files.
- The executable only printed a success message. The purported OpenAI client
  returned a hard-coded `done` response without making a request.

The user authorized completing and testing the fake vertical slice before
starting real LLM or MCP integration.

## 3. Implemented components

| Location | Responsibilities and principal types |
| --- | --- |
| `cmd/grodt/main.go` | Runnable two-step fake account inspection demo; interrupt cancellation |
| `internal/state` | `AgentState`, `WorldState`, `MemoryEntry`, `Intent`, typed `StatePatch`, `IntentUpdate`, `Store`, `MemoryStore`, deep snapshot copying, atomic patch validation/application |
| `internal/observation` | Transient `Observation` and `ErrorInfo` |
| `internal/tools` | Provider-neutral tool definitions/calls/results; `Provider`, `Registry`, `ProviderRegistry`; discovery snapshot, namespacing, routing, JSON Schema validation, `FakeProvider`, explicit `FatalError` |
| `internal/llm` | Model-neutral `Client`, request/response types, recording `FakeClient` |
| `internal/agent` | `Agent`, `LLMAgent`, recording `FakeAgent`, `StepInput`, `Decision`, `InvalidDecisionError`; full context construction and strict JSON fallback decoding |
| `internal/validation` | Decision and state-patch validators, exact-name permission allowlist, composable action validators |
| `internal/runtime` | `Runtime.Step`, thin `Runtime.Run`, `StepResult`, transient observation feedback, injectable clock, optional trusted `Reducer` interface |

The duplicate generic `internal/types` definitions were consolidated into
`internal/state`. The fake OpenAI transport stub was removed rather than left
available as an apparently functional client.

Current dependency direction:

```text
cmd/grodt -> runtime -> agent -> llm
                    -> state
                    -> tools
                    -> observation
                    -> validation -> agent, state, tools
```

State has no provider/transport dependencies. Runtime contains no MCP,
OpenAI HTTP, Alpaca, or Uniswap implementation.

A JSON Schema dependency was added:
`github.com/santhosh-tekuri/jsonschema/v6 v6.0.2`.
Its transitive dependency is `golang.org/x/text v0.14.0`.

## 4. Runtime behavior and explicit design choices

A step loads state, reconstructs agent context, obtains and validates a decision,
validates/applies any typed patch, resolves and validates any tool call, executes
through the registry, validates output, creates an observation, invokes matching
trusted reducers, and saves state. `Run` repeats until `Done` or a fatal error.

The following choices are implemented and should be preserved or deliberately
revisited during subsequent design work:

1. **State ownership:** loads and saves copy all mutable state collections.
   Agents receive independent snapshots. Patches cannot express `World`,
   `Version`, or `UpdatedAt`.
2. **Patch rules:** duplicate identifiers, unknown removals/updates, conflicting
   operations, and inconsistent intent execution status/timestamps are rejected.
   Patch application is atomic. Memory replacement currently requires a removal
   step followed by an addition step; there is no update-memory operation.
3. **Failure rollback:** validation failures discard the proposed patch. After a
   provider attempt, an otherwise valid patch is retained even if execution fails
   or the output is invalid. These outcomes become feedback observations.
4. **Versions:** every successfully saved step advances the version once and sets
   `UpdatedAt`, including terminal and recoverable-failure steps. The store itself
   does not generate revisions.
5. **Permissions:** `validation.NewValidators()` denies every tool. Exact public
   names must be allowed explicitly, such as
   `validation.NewValidators("fake.get_account")`. Additional action policies run
   before execution. No domain-specific trading risk implementation exists yet.
6. **Registry:** `registry.Register(ctx, provider)` discovers once and registers
   atomically. Public names are `provider.remote_name`; providers receive the
   original remote name. Duplicate providers/tools are rejected. Listings are
   sorted and returned definitions are copied.
7. **Schemas:** arguments must be JSON objects and satisfy the advertised input
   schema when supplied. Output must be valid JSON and satisfy the output schema
   when supplied. Local schema references work; external schema references are
   rejected without fetching remote or local files.
8. **Decision semantics:** `intent` is descriptive and may accompany a tool call.
   It is never parsed into executable behavior. `done` cannot accompany a tool
   call. A nonterminal decision must call a tool or propose a nonempty patch.
9. **LLM context:** the system message contains agent instructions; a separate
   data message contains the goal, full state, observation including errors, and
   discovered tool definitions/schemas. Every call reconstructs this context.
   JSON decoding rejects unknown fields, trailing JSON values, and invalid
   decision shapes. Native endpoint structured-output support is not implemented.
10. **Feedback:** successful output and recoverable failures reach the next
    decision. Observations are never automatically appended to memory. Feedback
    is installed only after saving state; a step without a new observation clears
    the previous observation.
11. **Trusted reduction:** only successful, schema-validated output reaches
    reducers. A test reducer proves trusted world-state updates and delivery of
    the resulting state to the next decision. No production provider-specific
    reducer is present.
12. **Fatal failures:** storage, tool discovery/listing, LLM transport, reducer,
    cancellation, and explicitly marked `tools.FatalError` failures stop the run.
    Ordinary provider errors become observations. There is no automatic retry.

## 5. Automated verification

The following checks passed during the implementation checkpoint:

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/grodt
git diff --check
```

The runnable demo produced:

```text
Fake vertical slice completed: 2 decisions, 1 tool call, state version 2
Second decision received observation: {"status":"active"}
```

Test files added:

- `internal/runtime/runtime_test.go`
- `internal/state/state_test.go`
- `internal/tools/registry_test.go`
- `internal/agent/agent_test.go`
- `internal/validation/validation_test.go`

Tests prove the fake tool-call -> observation -> next decision -> Done flow;
state isolation and atomic patches; full prompt context and strict decoding;
discovery, routing, and collisions; nested schemas and local references;
default-deny permissions; action rejection before provider execution;
provider-error recovery; rejection of invalid output before reduction;
trusted reduction and persistence; version updates; observation isolation and
IDs; cancellation before execution; and fatal storage/provider/reducer failures.

The fake boundaries expose `FakeAgent.Inputs`, `FakeClient.Requests`, and
`FakeProvider.Calls` so tests assert what actually crossed each boundary.
These tests establish deterministic orchestration behavior, not model intelligence
or live service interoperability.

## 6. Remaining limitations and deferred scope

- No real OpenAI-compatible HTTP client, endpoint configuration, authentication,
  or live model run has been implemented.
- No MCP client or server, external discovery, or external provider integration
  exists in the current implementation.
- No trading, domain-specific risk limits, or production account/position/order
  reducers have been implemented.
- State is in memory and does not survive process exit. Observation feedback is
  transient. Restart continuation is not proven.
- One active runtime writer should own a store. There is no cross-runtime
  compare-and-swap; the recording fakes are intended for sequential use.
- Tool execution and state saving are not transactional. If reduction or saving
  fails after an external side effect, the run stops; the caller must reconcile
  the external result before retrying. Durable journals and idempotency protocols
  are deferred.
- There are no run limits, backoff policies, structured run-event logging,
  pause/resume controls, skills, database storage, or multi-agent evaluation.
- JSON decoding uses Go's strict unknown-field/trailing-value checks; no claim is
  made that native JSON Schema constrained generation has been implemented.

The full architecture plan remains broader than this checkpoint.

## 7. Workspace and IDE configuration note

Changes are currently in the working tree, not a newly created commit or PR.
The prior MCP-centric source deletions were already present when this repair
began; the new runtime implementation builds on that existing work.

`.vscode/mcp.json` still declares a legacy `grodt-mcp` stdio server command pointing
to `bin/grodt-mcp`. That configuration was inspected but not changed as part of
this work. It does not describe the new fake runtime executable. The current
`cmd/grodt` program prints a demo result and does not implement the MCP protocol;
pointing an MCP stdio configuration at it would not make it an MCP server.

Any cleanup of that legacy IDE configuration should preserve the architectural
choice that Grodt is middleware, with MCP integration added through explicit
adapters later.

## 8. Recommended next checkpoint

Implement the plan's **second milestone: real LLM with the provider still fake**.

1. Add `internal/llm/openai.go` as a real OpenAI-compatible HTTP transport behind
   the existing `llm.Client` interface.
2. Add configurable base URL, model, and API key, with appropriate environment
   overrides. Keep model-specific behavior out of runtime and agent code.
3. Add transport tests using a local test HTTP server: request encoding,
   authorization, response decoding, HTTP errors, malformed responses, and
   cancellation/timeouts. Avoid logging credentials.
4. Decide how native structured output is configured for endpoints that support
   it while preserving the tested strict JSON fallback and runtime validation.
5. Retain the fake provider and explicit permission allowlist. Prove that a real
   model receives state/tools, requests the fake tool, receives its observation,
   and returns `Done`.
6. Report that evidence and stop at the second checkpoint before MCP integration.

A live run still requires the intended endpoint, model, and authentication
configuration; this report does not assume those details.

Suggested prompt to continue the ChatGPT conversation:

> Review this checkpoint against our original Loop Middleware Architecture Plan.
> The fake vertical slice is implemented and tested; real LLM and MCP integration
> remain deferred. Assess the explicit state, error-recovery, permission, and
> observation semantics above, then help specify the second milestone's endpoint
> configuration and OpenAI-compatible transport acceptance criteria. Preserve the
> provider-neutral runtime and keep MCP integration for the subsequent checkpoint.
