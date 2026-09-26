# 0001-grodt — OpenAI transport checkpoint

September 25, 2026. **Implementation, automated tests, and live acceptance passed.**
This supersedes the real-LLM limitations in the earlier `CHATGPT_HANDOFF.md`.
The subsequent read-only MCP integration is now complete; see [MCP_CHECKPOINT.md](MCP_CHECKPOINT.md).

## Scope and implementation

The model-neutral `llm.Client` now has a real `OpenAIClient` implementation in
`internal/llm/openai.go`, using Go's standard HTTP library. No additional module
dependency was needed. `internal/llm/config.go` provides API root, model, optional
API key, and positive request timeout configuration with environment overrides.
Runtime, agent, state, registry, and validators retain their existing boundaries;
no Qwen-specific or llama.cpp-specific types were added to runtime.

The implementation follows the
[official OpenAI Chat Completions API contract](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create):
POST an explicitly configured model and ordered messages to `/chat/completions`
under the API root, then read assistant text from the completion envelope.
The requested system and user/data messages are preserved.

The transport uses nonstreaming requests, context cancellation, and a timeout
covering the request through body reading. It accepts unknown envelope metadata
for compatibility, while requiring exactly one assistant message with nonempty
string content. A supplied `finish_reason` must be `stop`; missing/null values
are tolerated for compatible endpoints that omit that metadata. Invalid JSON,
trailing JSON, malformed envelopes, empty text, and abnormal finish reasons fail.
The agent still performs strict JSON decision decoding afterward.

Response reads are limited to 1 MiB plus one detection byte, including decoded
HTTP compression. Oversize responses fail before JSON parsing. Every non-2xx
status returns a `TransportError`; redirects are not followed. No retries or
native structured-output request parameters were introduced.

API keys are used only in Authorization headers, which are absent for empty
keys. The client does not log. Transport error messages omit headers, URLs, raw
bodies, and underlying error text, while preserving error unwrapping for timeout
and cancellation classification. Base URLs containing user credentials, query
parameters, or fragments are rejected. Invalid header characters in keys are
rejected without printing the key.

## Configuration and command

| Field | Environment override | Alias | Default |
| --- | --- | --- | --- |
| BaseURL | `GRODT_LLM_BASE_URL` | `OPENAI_BASE_URL` | `http://localhost:8080/v1` |
| Model | `GRODT_LLM_MODEL` | `OPENAI_MODEL` | No model default |
| APIKey | `GRODT_LLM_API_KEY` | `OPENAI_API_KEY` | Empty |
| RequestTimeout | `GRODT_LLM_REQUEST_TIMEOUT` | None | `2m` |

GRODT variables override their OPENAI aliases, including explicitly empty values.
Explicit `-base-url`, `-model`, and `-request-timeout` CLI flags override loaded
configuration. Timeout environment values must parse as positive Go durations.
API keys have no CLI flag. There is no implicit `.env` loading.

`go run ./cmd/grodt` retains the fully fake demo.
`go run ./cmd/grodt -mode llm` runs the real LLM acceptance scenario with the fake
provider. The model name can be provided through environment or `-model`.

The LLM acceptance command sets an account-inspection goal, permits only
`fake.get_account`, and invokes the normal `Runtime.Run` loop. A command-local
recording client enforces a two-request budget. After termination, assertions
check the tool context, actual provider call count, observation in the second
request, and state version. It prints success only if all seven criteria pass.
A model that returns Done without calling the tool does not pass acceptance.

## Automated results

Passed:

```text
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

New tests:

- `internal/llm/config_test.go`: environment precedence, explicitly empty auth,
  invalid URLs/models/timeouts, invalid auth headers, and safe configuration errors.
- `internal/llm/openai_test.go`: POST path with root and prefixed/trailing-slash
  API roots; model; message preservation; auth present/absent; successful and
  minimal compatible responses; malformed JSON/envelopes; empty text; non-2xx
  statuses; exact size boundary and oversize chunked response; cancellation
  before a request and during a request; timeout before headers and while reading
  the body; redirects; safe network diagnostics.
- `cmd/grodt/main_test.go`: actual HTTP transport through the agent/runtime/fake
  provider loop; both requests' context; seven acceptance checks; early Done
  rejection; two-request budget; CLI/environment precedence; preserved fake demo;
  and fatal transport error behavior.

Existing state, runtime, schema, registry, agent, and validation tests still pass.

## Live acceptance evidence

The existing `OPENAI_BASE_URL` and `OPENAI_API_KEY` environment configuration was
used without printing the key. A read-only model listing returned one model:

```text
unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q8_0
```

The endpoint was the configured local server at `http://192.168.1.231:8080/v1`.
The model name was supplied only as command configuration:

```sh
go run ./cmd/grodt -mode llm -model 'unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q8_0'
```

Observed process exit code: **0**. Output:

```text
PASS: LLM received fake.get_account
PASS: LLM requested fake.get_account
PASS: Fake provider executed exactly once
PASS: Tool result became a successful Observation
PASS: Second LLM request contained that Observation
PASS: LLM returned Done
PASS: Runtime exited successfully; state version 2
```

This was a live model run, separate from the local HTTP test-server runs.
The external tool was still the fake account provider; no real account or
trading service was contacted. A successful live run demonstrates this endpoint
and model combination; it does not guarantee identical behavior from every model.

## Remaining scope

Native structured-output generation, streaming, HTTP retries, general runtime
step limits, durable persistence, and external MCP integration remain deferred.
The two-request budget is specific to the acceptance command, not a new runtime
lifecycle feature. MCP client/server code and the legacy `.vscode/mcp.json` were
not changed by this milestone.

The next planned checkpoint is a provider-neutral `mcpclient.Provider` backed by
one read-only MCP server, with dynamic discovery and the existing schema,
permission, and observation flow. Trading remains outside that checkpoint.
