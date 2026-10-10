# Component 005: MCP tools

GRODT can discover and invoke tools from configured remote MCP servers without
provider or domain code in core. With no `mcp.servers`, ordinary CLI generation
and evaluation retain their existing behavior. This component uses the official
Go MCP SDK v1.8.0 and its protocol negotiation over Streamable HTTP. It does not
implement stdio, the legacy SSE transport, resources, prompts, subscriptions,
dynamic tool discovery, OAuth, or secret resolution.

## Configuration

Add an optional section to the same configuration file as the existing OpenAI
configuration:

```yaml
mcp:
  servers:
    - name: example
      transport: http
      url: "https://example.com/mcp"
      environment:
        MCP_TOKEN: "replace-me"
        TENANT: "example-tenant"
        LOCAL_OPTION: "not-transmitted"
      http:
        headers:
          Authorization:
            from_environment: MCP_TOKEN
            prefix: "Bearer "
          X-Tenant:
            from_environment: TENANT
```

Environment keys have no provider-specific meaning. Only explicitly bound values
become headers, with the optional literal prefix. Unbound values stay local; GRODT
does not copy process environment variables into this map, transmit the complete
map, or turn it into initialization metadata. Existing OpenAI YAML/OS precedence
is unchanged. A future secrets resolver can populate these values before connection.

Secret/sensitive resolved connection values and values originating from the opaque
`environment` map or header bindings must not enter catalogs, model inputs, State,
task inputs, or diagnostics. Non-secret identity and metadata, such as the configured
server name, original tool name, operation identity, and failure category, remain
available. SDK logs and response-bearing errors are not forwarded. Known configured
value echoes in discovered metadata, requested arguments, or returned content are
rejected before admission rather than rewritten into accepted domain data. This
check is conservative: configuration values that also occur in otherwise legitimate
content can make that content unusable.

Duplicate server names, invalid URLs, unsupported transports, unresolved bindings,
invalid headers, and overrides of transport-managed headers fail initialization.
MCP redirects are rejected. This does not change the existing HTTP HostRequest
policy of at most three redirects per logical host execution.

## Startup and tool identity

Configured servers connect sequentially in YAML order. GRODT collects every
pagination page, validates advertised input and output schemas with the existing
offline structural validator, and publishes the catalog only after every server
succeeds. Failure closes already-opened sessions. A valid empty toolset is allowed.
An unusable tool definition fails startup; the SDK's tool filtering cannot silently
reduce GRODT's catalog. Discovery is frozen for the run.

A tool is identified by the pair `(server name, original tool name)`; `server.tool`
is display text, not a string to split. Native provider names are deterministic,
collision-free aliases such as `grodt_tool_001`. Actual MCP invocation uses the
server's original name. Descriptions and schemas are data, not instruction authority.

## Execution and observation

Catalogs with more than 128 tools use model-visible pages of 126 MCP tools plus
`grodt_manage_plan` and `grodt_select_tool_page`. The planning tool stays callable
on every page. A compact page index names every discovered tool; the
model selects a page to see its schemas and invoke its tools. Selection must be
called alone and performs no external operation. Tool aliases stay stable and
all discovered tools remain accessible. Smaller catalogs are sent without paging. The local `grodt_manage_plan` tool
persists actor-authored intent and evaluated procedural outcomes; see
[task planning](general-state.md#model-authored-plans).
The selected page persists across cycles; navigation is limited to 32 selections
per generation and does not consume the MCP invocation budget.

Only agent generation receives native tools. Existing structured generation,
contract operations, and completion evaluation retain their contracts. A native
response contains final text or a batch of tool calls; text accompanying calls is
intermediate. Each continuation is part of the originating generation operation
and **does not start another outer agent cycle**. The next inference receives fresh
actionable state projection and the latest complete native tool-call/result
exchange. Older exchanges are replaced after the next batch; outer cycles start
a fresh native conversation. Historical payloads remain in tasks and the journal.

Before creating executable tasks or dispatching any call, GRODT checks the entire
returned batch: size, nonempty unique call IDs within the inference, known aliases,
argument JSON syntax and advertised schema, byte limits, and remaining run budget.
If a batch does not fit, **none of its calls execute**. For example, a batch of three
with two remaining invocations fails before dispatch. Other invalid batches receive
host-authored validation feedback so the model can return corrected native calls.
No rejected call executes. Correction does not parse or repair argument JSON.

Generation allows at most two failure corrections per operation. Tool markup
printed as text receives feedback that no tool executed and native tool calls are
required. Recoverable model HTTP failures (400, 422, 429, and 5xx) receive safe
status feedback; provider bodies are not copied into prompts or traces. Native
call/result pairs remain intact while correction feedback is replaced. Cancellation,
timeouts, authentication failures, sensitive-value rejection, exhausted invocation
budgets, and MCP protocol/transport failures remain terminal. Tool `isError` results
retain the server's accepted error message and continue to the next inference.

Calls become pending sibling tasks beneath the causal reasoning task and execute
sequentially. Agent invocation metadata is separate from module `HostWork`. Each
call records generation identity, inference turn, native call ID, original tool
identity, arguments, and outcome. The invocation counter advances immediately
before dispatch, including calls that fail. Later explicit model decisions may
invoke the same arguments again; GRODT does not claim remote exactly-once effects.

Successful MCP results preserve supported content blocks and optional structured
content. Text need not be JSON. Successful structured output must satisfy an
advertised output schema; `isError` results do not have to satisfy a success schema.
Resource links are retained as data and never fetched. Original JSON is retained
alongside SDK decoding to preserve large numbers and explicit null/false values.
Wire reads and accepted results are bounded; no silent truncation occurs.

The accepted outcome is retained in the current native exchange and the invocation
task **before** State admission. The normal event envelope supplies identity,
sequence, timestamp, causal task, and correlation containing `operation_id`,
`turn`, and `request_id`. Source uses the existing object representation:
`{"kind":"mcp","id":"example"}`, where `id` is the configured MCP server name.
Invocation identity remains in `task_id` and correlation. The event payload is:

```json
{
  "tool": "lookup",
  "result": {
    "content": [{"type": "text", "text": "observation"}],
    "structuredContent": {"value": 42},
    "isError": false
  }
}
```

`result` is the accepted MCP envelope. Its optional `structuredContent` accepts
any JSON value; absence and explicit null are distinct. The generic
[MCP tool state module](../modules/mcp-tool-state/README.md) retains the latest
result under `sources[source.id][tool]`. Native tool continuation messages and
invocation task outcomes retain their existing routing metadata representation.

All eligible modules process the event once. Partition mutations never become new
observations. Results remain available whether there are zero partitions or modules
return Ignored, Processed, Mutation, or Error. Existing module isolation/staleness
semantics remain unchanged.

Modules cannot initiate MCP dispatch. They may still request authorized HTTP host
work in response to an MCP observation. All modules finish the broadcast before
that work executes; HTTP result observations can produce further bounded HTTP work.
This preserves the existing capability and is not a network prohibition against
HTTP endpoints which happen to expose MCP. The invocation task remains causal
while this work drains. Its accepted result remains retained if HTTP budget
exhaustion or cancellation prevents further inference. Retention is in-memory for
the run, not durable replay.

Transport/protocol failures terminate the generation and stop undispatched
siblings. Malformed, oversized, or schema-invalid results are rejected without
admitting their contents as domain observations. Valid `isError` outcomes mark the
tool task failed but remain admitted and available to reasoning. Earlier successful
observations and partition commits are retained. Cancellation never authorizes a
new inference or dispatch.

## Named execution policy

| Limit | Initial value |
| --- | ---: |
| Servers | 8 |
| Discovery pages per server | 32 |
| Tools across catalog | 256 |
| Serialized catalog | 1 MiB |
| Calls per native batch | 8 |
| MCP invocations per run | 32 |
| Initialization and discovery per server | 30 seconds |
| Invocation | 30 seconds |
| Arguments per call | 64 KiB |
| Accepted result per call | 1 MiB |
| HTTP response wire bytes, including framing/notifications | 1 MiB + 64 KiB |

Earlier caller deadlines prevail. MCP's run invocation budget is independent of
both the 20 outer cycles and the module HTTP budget of 32 host executions.
`MaxStructuredAttempts = 3` continues to govern existing structured operations;
it does not govern native tool calls. SDK reconnection and multi-round-trip retries
are disabled. There is no automatic resubmission of `tools/call`.

## Verification

```sh
go test ./...
go test -race ./...
```

Deterministic tests use arbitrary local servers, including the pinned SDK's modern
server, duplicate tool names across servers, paginated catalogs, strict startup
cleanup, invalid batches, budget preflight, result fidelity, cancellation, and
failure isolation. They exercise a real WASM partition, continuation without
partition mutation, sequential task lineage, and MCP-triggered HTTP chains.

The separate live milestone uses the configured LLM, a harmless local MCP server
configured entirely through YAML, and the generic MCP tool state WASM module:

```sh
go test -tags live ./cmd/grodt -run '^TestLiveMCPFeedbackLoop$' -count=1 -v
```

It reads `config.yaml` or `GRODT_LIVE_CONFIG` for the LLM. It does not connect to
configured production MCP servers. The objective is to retrieve an initially
unknown marker, commit the external observation through WASM, and report the
marker after the next inference receives the committed state. An independent
injected evaluator checks these conditions. Responses are logged; the test asserts
causal outcomes rather than a prescribed action sequence. Missing LLM configuration
skips the live test and does not establish milestone acceptance.

Observed acceptance on 2026-10-02: the live model requested one discovered tool,
then reported the unknown marker after its result was admitted and the reference
WASM partition committed. The run completed in one outer cycle, with one MCP
invocation and two LLM requests. This records one successful run, not an expected
action sequence. The deterministic suite, race suite, static analysis, and diff
whitespace checks passed.

## Outstanding policy clarification

The conservative configured-value echo checks remain enabled. They can reject
independently sourced non-secret metadata or domain content that coincides with
an opaque option. For example, an unused option valued `1` can match a generated
tool alias. This creates a tension between arbitrary opaque configuration and
preventing every configured-value echo from entering the processing loop.
Removing these checks was rejected by automatic approval review because doing so
would permit echoed connection values without an equivalent safeguard. No removal
was applied. Finalizing this policy requires user clarification; the passing tests
above do not resolve that architectural question.
