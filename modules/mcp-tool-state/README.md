# MCP tool state module

This import-free `grodt.state/v1` module maintains a run-scoped materialized view
of the latest admitted result for each MCP source/tool pair. It has no provider,
domain, or execution-policy knowledge and needs no module configuration.

The repository's default `config.yaml` selects this module:

```yaml
state:
  definition: modules/mcp-tool-state/state.json
```

Manifest paths in YAML resolve relative to the configuration file. Run from the
repository root with `go run ./cmd/grodt 'your request'`. MCP servers must also be
configured to produce tool observations; loading the module alone does not connect
to a server.

The CLI can override the configured manifest (relative CLI paths use the working
directory). `--state-definition=` explicitly disables configured partitions.
For example:

```sh
go run ./cmd/grodt --config config.yaml \
  --state-definition modules/mcp-tool-state/state.json \
  'Use the available tools to answer my request.'
```

The manifest loads the `mcp_tools` partition, initialized as `{"sources":{}}`.
The state lives in memory across inferences within one run. It is not durable
across restarts. No task planner, provider allow-list, or persistence is added.

## Input and retained state

The existing event envelope remains authoritative. Applicable events have
`source.kind == "mcp"`; `source.id` is the configured, human-readable server name.
The payload contains `tool` and `result`. Sequence, timestamp, task identity and
correlation come from the ordinary GRODT event envelope, not from the MCP server.

```json
{
  "sources": {
    "example": {
      "lookup": {
        "sequence": 42,
        "timestamp": "2026-10-03T21:30:00Z",
        "correlation": {
          "operation_id": "run/generation/1",
          "turn": 1,
          "request_id": "call-1"
        },
        "result": {
          "content": [{"type": "text", "text": "observation"}],
          "structuredContent": null,
          "isError": false
        }
      }
    }
  }
}
```

The MCP result envelope is structurally constrained. The partition schema mirrors
Component 005's admission schema; a deterministic test detects drift. The reducer
relies on admission and candidate validation for the detailed content-block
contract. It checks the fields it needs to reduce an applicable event.

`structuredContent` is optional and unconstrained (`{}` in JSON Schema). Objects,
arrays, strings, numbers, booleans and null are preserved as admitted JSON spans.
The module does not convert numbers to floating point, parse text as domain JSON,
select a primary content block, or remove error flags. Missing structured content
remains missing; explicit null remains null. JSON formatting outside the module
may be compacted by the host without changing the values.

New or strictly newer sequence numbers produce a complete replacement candidate;
GRODT validates and atomically commits it. Older or duplicate observations return
`Ignored`, as do non-MCP events. Applicable events with unusable structure or a
resource-limit failure return `Error`. The module never emits `Processed`, host
requests, or observations. An admitted `isError:true` result supersedes an older
success just like any other newer result.

The key deliberately excludes invocation arguments. Two parameterized calls to
the same tool overwrite the same entry; results of different sources or tools
remain independent. JSON property names are compared as decoded strings, so
escaped names, Unicode, quotes, slashes and tildes do not create path ambiguities.

## Staleness remains a base-state question

Host behavior is unchanged: processing failure retains the last valid partition
and marks it stale; a committed mutation clears partition staleness. Updating one
source/tool does not demonstrate recovery or freshness of a different entry that
previously failed to update. The module introduces no per-entry recovery policy
and does not reinterpret the host's synchronization assertion. This mismatch is
documented for a future base-state design pass. Ignored duplicate/older results
never clear staleness.

## Build and verification

The checked-in WASM binary allows ordinary tests to run without a C toolchain.
To rebuild it from the repository root with Clang and a compatible WASM linker:

```sh
clang --target=wasm32 -O2 -nostdlib -fno-builtin \
  -fuse-ld=/usr/bin/wasm-ld-15 \
  -Wl,--no-entry -Wl,--max-memory=67108864 \
  modules/mcp-tool-state/module.c \
  -o modules/mcp-tool-state/mcp-tool-state.wasm
```

The module uses bounded static buffers: 8 MiB input, 4 MiB output (including the
mutation envelope), 262,144 JSON tokens, and nesting depth 256. The existing host
memory and execution deadline limits also apply. Capacity exhaustion fails the
operation; there is no truncation or eviction. Tokenization preserves opaque JSON
values and only decodes strings for structural/property-name comparisons. Each
host invocation instantiates the module afresh; there is no hidden persistent state.

```sh
go test ./...
go test -race ./...
go test -tags live ./cmd/grodt -run '^TestLiveMCPFeedbackLoop$' -count=1 -v
```

The live test uses the configured LLM with a harmless local MCP fixture and this
module. It does not call a production MCP server. It verifies an unknown external
marker is admitted, committed into the generic source/tool view, and available to
the subsequent inference. Ordinary deterministic tests need no external LLM.

Observed live acceptance on 2026-10-03: the model invoked the fixture tool twice,
then reported the retrieved marker. Both results were admitted; the second
superseded the first source/tool entry. The run completed in one outer cycle with
three LLM requests. This is an observed run, not an expected action sequence.
