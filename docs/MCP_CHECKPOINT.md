# Checkpoint 3 — Read-only MCP integration

September 25, 2026. **Completed: all ten live acceptance criteria passed for both the reference time server and Alpaca paper account reading.**

## Scope and architecture

One configured MCP server per run now sits behind the existing provider-neutral
`tools.Provider` / `tools.Registry` boundary, using stdio only:

```text
Configured OpenAI-compatible LLM
  -> LLMAgent
  -> Runtime
  -> tools.Registry
  -> mcpclient.Provider
  -> real MCP server (time or Alpaca paper)
  -> ToolResult
  -> Observation
  -> next LLM request
  -> Done
```

The original acceptance run used the credential-free time reference server.
After the user supplied paper credentials and explicitly approved sending the
account observation to the configured local LLM, a follow-up run also passed
against the real Alpaca paper account API through the local Alpaca MCP checkout.
Only `alpaca.get_account_info` was permitted. No account/order/wallet mutation was
requested or executed. The checkout's source was not modified; its pinned
runtime dependencies were installed into its local virtual environment.

The Alpaca-specific evidence, including troubleshooting reads, is recorded below.
No credentials, balances, account identifiers, or raw account payloads are included
in this report.

Runtime, state, agent, observation, LLM transport, registry, and validation
implementation files were unchanged. Generic types remain in `internal/tools`.
No trading, production reducers, durable persistence, native structured output,
MCP server, skills, retries/backoff, or multi-agent behavior was added.

## Files added or changed

Added:

- `internal/mcpclient/config.go`: strict, size-bounded JSON configuration;
  subprocess command, arguments, environment selection, and startup timeout.
- `internal/mcpclient/client.go`: SDK session ownership, safe error types,
  cancellation, and idempotent close.
- `internal/mcpclient/discovery.go`: paginated discovery, metadata/schema mapping,
  duplicate detection, snapshot caching, and defensive copies.
- `internal/mcpclient/provider.go`: remote calls, error classification, and
  provider-neutral JSON result normalization.
- `internal/mcpclient/process_unix.go`: process-group isolation and descendant
  cleanup. `process_other.go` explicitly rejects unsupported operating systems.
- `internal/mcpclient/client_test.go`: a local stdio protocol subprocess and
  adapter/runtime integration tests.
- `cmd/grodt/mcp.go`: configurable, one-tool, ten-criterion acceptance command;
  failure diagnostics include only the runtime feedback category and call count.
- `cmd/grodt/mcp_test.go`: end-to-end local HTTP LLM + stdio MCP acceptance tests.
- `configs/mcp-time.json`: the real reference server configuration.
- `configs/mcp-alpaca-paper.json`: paper-only account toolset configuration;
  credentials are inherited by variable name and never stored in the file.
- `docs/MCP_CHECKPOINT.md`: this report.

Changed:

- `cmd/grodt/main.go`: adds `-mode mcp`, `-mcp-config`, `-mcp-tool`, and `-goal`.
  Existing fake and LLM modes remain available.
- `go.mod` / `go.sum`: SDK and its required transitive dependencies.
- `README.md`: configuration, commands, boundaries, and current checkpoint.
- `docs/OPENAI_TRANSPORT_CHECKPOINT.md`: links to this subsequent checkpoint.

## Library, transport, and discovery

Library: **`github.com/modelcontextprotocol/go-sdk v1.8.0`**, the
[official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).
Transport: **stdio**, via the SDK's `CommandTransport`.

The adapter requests the legacy `2025-11-25` handshake, allowing the SDK to
negotiate supported legacy versions. This avoids introducing newer stateless
protocol behavior into this checkpoint. SDK automatic multi-round-trip handling
is explicitly disabled; no call retry middleware is enabled.

Startup connects and initializes the session. Registry registration then invokes
`Provider.Discover`, which reads all tool-list pages and caches a complete
snapshot. Duplicate remote names and repeated pagination cursors fail; partial
results are not registered. The adapter has a defensive 1,000-page limit.

Namespacing remains entirely in the existing registry:

```text
remote get_current_time + provider time -> public time.get_current_time
public time.get_current_time -> remote get_current_time on execution
```

No remote tool definition or schema is hard-coded in runtime or agent code.
The command selects the reviewed tool by name, verifies it was discovered, and
passes exactly that public name to the existing default-deny permission validator.
Other discovered tools do not gain execution permission.

## Schemas and result normalization

Advertised input schemas and optional output schemas are serialized into the
existing `ToolDefinition` fields. No output schema is created when absent.
Registry schema compilation and runtime input/output validation are unchanged.
Existing restrictions on external schema references still apply.

Normalization rules:

1. Prefer `structuredContent` when present; preserve the whole value without
   stripping provider-specific security or data envelopes.
2. Otherwise, preserve a single text block containing valid JSON as JSON data.
3. Otherwise, encode all content blocks in a JSON `content` envelope, retaining
   text and other content types rather than treating prose as an executable action.

The registry restores the public tool name on the returned `ToolResult`.
Arguments cross the SDK boundary as raw JSON; tests verify remote naming and an
integer larger than the exact float64 range is forwarded unchanged.

The live time server advertised an input schema and **no output schema** for the
selected tool. Its JSON text output became observation data without inventing an
output contract. Tests separately verify preservation of an advertised output
schema and structured-content normalization.

## Error and security behavior

- MCP `isError` results return ordinary provider errors. Runtime converts them
  into `tool_execution_error` observations for the next model decision.
- JSON-RPC invalid-parameters errors (`-32602`) are also recoverable, with the
  numeric code preserved and remote message/data omitted.
- Additional-input requests return a recoverable unsupported-input error, with
  no automatic re-execution.
- Connection/session failures and other protocol failures use `tools.FatalError`.
- Discovery failure is a startup failure. Context cancellation/deadline errors
  retain their normal classification.

Raw server error text is withheld to avoid propagating credentials or sensitive
payloads into logs. This deliberately sacrifices detailed remote diagnostics;
feedback still identifies the failure category/code. Tool result data is sent
only through the normal observation-to-model path and is not printed by the CLI.
Server stderr and SDK logs are discarded.

The child inherits a small process-environment baseline. Any extra credential
variables require explicit `inherit_env` configuration. Commands execute without
a shell; the sample time server receives no account credentials.

## Cancellation and cleanup

Each call uses its caller context and the provider lifetime. Call cancellation
propagates through the SDK's MCP cancellation notification. Canceling the
connection's lifetime context also initiates close.

Close is concurrency-safe and idempotent. It cancels outstanding calls, closes
the SDK session, and lets `CommandTransport` close stdin and wait for the direct
subprocess. If necessary, the SDK escalates to termination and killing. Remaining
members of the isolated Unix process group are terminated afterward. Failed
startup also cleans up the process group.

Tests verify normal direct-child reaping, cancellation, startup timeout,
concurrent/repeated close, and termination of a spawned descendant. Descendants
outside the original process group are not supervised; this is not a sandbox for
untrusted executables. Non-Unix systems are explicitly unsupported by this
checkpoint's supervisor.

The live acceptance command reports success only after explicit successful
close. A post-run process check found no remaining MCP time server process.

## Automated verification

Before the live run, all required commands passed:

```text
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

No automated test requires a live model, account credentials, an installed Python
MCP server, or an external service. Tests run local HTTP handlers and child copies
of the Go test binary speaking stdio JSON-RPC.

Coverage includes one-tool and paginated discovery; schema mapping and absent
output schemas; public/remote names; collisions and atomic registration;
execution routing and argument forwarding; result normalization; recoverable
MCP failures becoming runtime observations; fatal connection/protocol failures;
default-deny permissions preventing remote execution; cancellation notifications;
startup timeout; close and cleanup; sensitive output suppression; and all ten
acceptance conditions through the actual transport boundaries.

The acceptance tests also reject early Done, requests for an undiscovered tool,
repeated calls, and LLM transport failure. Server processes are reaped on both
successful and unsuccessful acceptance paths.

## Original live acceptance: time reference server

Real read-only server: **`mcp-server-time==2026.8.18`**, the published
[MCP time reference server](https://github.com/modelcontextprotocol/servers/tree/main/src/time).
It was launched using `uvx` under the checked-in configuration.

Real model: `unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q8_0`, supplied only as
configuration, using the previously configured OpenAI-compatible local endpoint.
No model-specific code was introduced.

Command:

```sh
go run ./cmd/grodt -mode mcp \
  -mcp-config configs/mcp-time.json \
  -mcp-tool get_current_time \
  -goal 'Read the current time in America/Los_Angeles.' \
  -model 'unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q8_0'
```

Observed exit code: **0**. Output:

```text
Discovered tools: 2
Selected public tool: time.get_current_time
Input schema: true; output schema: false
PASS 1: Grodt connected to the MCP server
PASS 2: Tools were discovered dynamically
PASS 3: Selected read-only tool exposed under its namespaced public name
PASS 4: Real LLM received the discovered tool definition
PASS 5: Real LLM requested that tool
PASS 6: MCP provider executed the remote tool exactly once
PASS 7: Result became a successful Observation
PASS 8: Second LLM request contained that Observation
PASS 9: LLM returned Done
PASS 10: Runtime exited successfully and MCP resources closed cleanly
```

## Alpaca paper live acceptance

Server: local `alpaca-mcp-server` **2.3.2**, using its frozen dependency set
(including FastMCP **3.4.7**) and the same stdio adapter. Configuration:
`configs/mcp-alpaca-paper.json`.

- `ALPACA_PAPER_TRADE=true` selects the paper API.
- `ALPACA_TOOLSETS=account` limits the server toolset. The server also includes
  documentation tools and an account-configuration update tool; discovery alone
  grants none of them execution permission.
- Only `alpaca.get_account_info` is allowed by Grodt. The actual remote tool name
  is `get_account_info`, not `get_account`.
- Credentials are inherited as `ALPACA_API_KEY` and `ALPACA_SECRET_KEY`.
- **11 tools** were discovered dynamically.
- Both input and output schemas were advertised and preserved. The account
  input is an empty object. Its advertised output schema is the permissive
  `{ "type": "object", "additionalProperties": true }`.
- The server's `_alpaca_mcp_security` / `data` envelope was retained. No schema
  bypass, provider-specific unwrapping, or runtime/agent changes were needed.

The user explicitly approved sending the full account-read observation to
`http://192.168.1.231:8080/v1`. The credentials were supplied via hidden terminal
input and held in the execution environment only. They were not written to an
.env file, MCP configuration, source, report, or application log. The original
credential values supplied in the conversation are not reproduced here.

With credentials set locally, the successful run is reproducible using:

```sh
uv sync --frozen --no-dev --project alpaca-mcp-server
go run ./cmd/grodt -mode mcp \
  -mcp-config configs/mcp-alpaca-paper.json \
  -mcp-tool get_account_info \
  -goal 'Read the Alpaca paper account status once. Do not modify the account, create orders, or call any other tool.' \
  -model 'unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q8_0'
```

Observed exit code: **0**. Output:

```text
Discovered tools: 11
Selected public tool: alpaca.get_account_info
Input schema: true; output schema: true
PASS 1: Grodt connected to the MCP server
PASS 2: Tools were discovered dynamically
PASS 3: Selected read-only tool exposed under its namespaced public name
PASS 4: Real LLM received the discovered tool definition
PASS 5: Real LLM requested that tool
PASS 6: MCP provider executed the remote tool exactly once
PASS 7: Result became a successful Observation
PASS 8: Second LLM request contained that Observation
PASS 9: LLM returned Done
PASS 10: Runtime exited successfully and MCP resources closed cleanly
```

Troubleshooting record:

1. The first full Alpaca attempt did not pass: after one remote execution, the
   model requested another call. The command's one-call guard blocked that second
   execution. The initial diagnostic did not establish why the model repeated.
2. An isolated read through the real MCP server confirmed HTTP **200**, no MCP
   tool error, the security envelope, and an account status field. Only these
   checks were printed, not the account data.
3. Safe CLI failure diagnostics were added to report provider call count and the
   runtime feedback category without printing observation/error payloads. The
   full tests, race tests, vet, and whitespace checks passed again.
4. A fresh full run then passed all ten criteria with unchanged goal, model,
   account response handling, and permission policy.

There were **three read-only account executions across troubleshooting**: one in
original acceptance, one isolated diagnostic read, and one in successful
acceptance. The successful acceptance run itself executed the MCP tool exactly
once. There were no automatic retries and no mutating API operations.

The successful run establishes this live integration, not deterministic model
behavior on every run. The broad advertised output schema validates the envelope
as an object; it does not establish detailed account-field correctness. Trusted
account reduction remains a separate milestone.

## Protocol observations and next step

- MCP output schemas are optional; absence is preserved, not interpreted as a
  reason to invent a schema or disable JSON normalization.
- Text JSON and structured content are both encountered in MCP integrations.
  Normalization handles both, and runtime validation remains authoritative.
- Serializing an observation into an LLM prompt can change whitespace/escaping.
  Acceptance compares JSON values, preserving numeric precision, instead of
  rejecting equivalent data due to formatting changes.
- SDK v1.8.0 enables automatic multi-round-trip handling by default. It was
  explicitly disabled to honor the no-retry requirement.
- Server read-only annotations are hints, not permission grants. The live run
  used a reviewed read-only reference server and one explicit tool permission.
- Both live scenarios passed without changing the MCP adapter or runtime schema
  policy. Alpaca's account output schema is permissive and its trust envelope
  remains intact; future reducers must validate the specific fields they consume.

Stop point: checkpoint 3, including Alpaca paper account reading, is complete.
Recommended next step is to separately specify and test a minimal trusted account
reducer against the preserved envelope and explicit field-level validation,
followed by read-only positions when authorized. No production reducers or
trading were started here.
