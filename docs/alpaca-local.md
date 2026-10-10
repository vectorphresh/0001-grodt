# Local Alpaca MCP setup

The local checkout in `alpaca-mcp-server/` runs over Streamable HTTP at
`http://127.0.0.1:8000/mcp`. GRODT's `config.yaml` points to this endpoint and
loads `modules/mcp-tool-state/state.json`.

Alpaca credentials are held by the server in `alpaca-mcp-server/.env.local`,
with file permissions `0600`. The file is ignored by that repository. The server
expects `ALPACA_API_KEY` and `ALPACA_SECRET_KEY`; the supplied `APCA_API_KEY_ID`
and `APCA_API_SECRET_KEY` values were mapped to those names. They are not stored
in GRODT's MCP environment/header bindings. `ALPACA_PAPER_TRADE=true` selects the
paper endpoint.

The current server runs as the transient user service `grodt-alpaca-mcp`:

```sh
systemctl --user status grodt-alpaca-mcp
systemctl --user restart grodt-alpaca-mcp
systemctl --user stop grodt-alpaca-mcp
```

The transient unit is not installed for automatic startup after reboot. To start
the server manually in a terminal after reboot, run from the repository root:

```sh
cd alpaca-mcp-server
uv run --frozen alpaca-mcp-server --env-file .env.local \
  --transport streamable-http --host 127.0.0.1 --port 8000
```

Use either the existing service or the foreground command; both bind the same
port. From another terminal in the GRODT repository root:

```sh
go run ./cmd/grodt 'Read my paper account information and summarize it. Do not place, change, or cancel orders.'
```

Verification performed: 72 tools discovered; a read-only `get_account_info` call
succeeded with an ACTIVE paper account; GRODT accepted the result and the generic
WASM module committed it under `mcp_tools.sources.alpaca.get_account_info` in the
verification run. No orders were placed, modified, or cancelled. The verification
snapshot was in memory only and does not persist into subsequent GRODT runs.
