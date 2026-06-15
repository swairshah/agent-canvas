# canvas-mcp

A single-binary stdio **MCP server** that exposes the Agent Canvas to any MCP
client with local-server support: **Claude Desktop, Claude Code, Codex CLI**.

(ChatGPT is intentionally out of scope: it supports only *remote* MCP and only
on the web, so it can't run a local stdio tool. For ChatGPT, the human just
opens the canvas link in a browser.)

## Tools

- `canvas_create {prompt}` — create a canvas; returns a link to open & draw.
- `canvas_status {id}` — check pending/done.
- `canvas_wait {id, timeout?}` — block until the human submits, then return the
  drawing as an **image** the model can see.

## Build

```sh
go build -o canvas-mcp .
```

Produces a static binary — copy it to your laptop, no runtime needed.

## Auth: where does the agent run?

- **On the VM** (Claude Code/Codex in the VM shell): default
  `CANVAS_BASE=http://localhost:8000`, no token.
- **On your laptop** (Claude Desktop / laptop CLI): point at the proxy and
  supply a VM bearer token:
  ```sh
  ssh exe.dev ssh-key generate-api-key --vm=<your-vm> --label=canvas --exp=30d
  ```
  Set `CANVAS_BASE=https://canvas.exe.xyz` and `CANVAS_TOKEN=<token>`.

## Claude Desktop config

`~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) / `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "agent-canvas": {
      "command": "/absolute/path/to/canvas-mcp",
      "env": {
        "CANVAS_BASE": "https://canvas.exe.xyz",
        "CANVAS_TOKEN": "exe0....",
        "CANVAS_PUBLIC": "https://canvas.exe.xyz"
      }
    }
  }
}
```

## Claude Code

```sh
claude mcp add agent-canvas /absolute/path/to/canvas-mcp \
  -e CANVAS_BASE=https://canvas.exe.xyz \
  -e CANVAS_TOKEN=exe0....
```

(Running Claude Code *on the VM*? Drop the env and it defaults to localhost.)

## Codex CLI

`~/.codex/config.toml`:

```toml
[mcp_servers.agent-canvas]
command = "/absolute/path/to/canvas-mcp"
env = { CANVAS_BASE = "https://canvas.exe.xyz", CANVAS_TOKEN = "exe0...." }
```

## Smoke test

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | CANVAS_BASE=http://localhost:8000 ./canvas-mcp
```
