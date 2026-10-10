# Local Alpha Vantage MCP setup

The configured endpoint is `http://127.0.0.1:8001/mcp`. The local
`alpha_vantage_mcp/mcp/grodt_local_server.py` launcher reads server-owned
credentials from `alpha_vantage_mcp/.env.local`.

The server runs as the transient user service `grodt-alpha-vantage-mcp`:

```sh
systemctl --user status grodt-alpha-vantage-mcp
systemctl --user restart grodt-alpha-vantage-mcp
```

The transient service does not start automatically after reboot. To launch the
server in a terminal from the GRODT repository root:

```sh
cd alpha_vantage_mcp/mcp
../.venv/bin/python grodt_local_server.py
```

Use the service or the terminal launcher, since both bind the same port.
A refused connection to port 8001 means the server must be started before GRODT.
