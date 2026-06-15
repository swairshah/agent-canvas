# Agent Canvas

A TLDraw-backed canvas that a command-line coding agent can pop up for a human.

## Flow

1. Agent runs `canvas-cli request "Sketch the blog header"`.
2. CLI creates a session and prints a link: `https://canvas.exe.xyz/c/<id>`.
3. You open it on your phone (exe.dev proxy authenticates you), draw on the
   TLDraw canvas, and tap **Send**.
4. On Send, the browser exports a PNG + the TLDraw snapshot and POSTs them;
   the session flips to `done`.
5. The CLI, which has been polling, sees `done`, downloads the PNG (so an LLM
   can *see* the drawing) and the snapshot JSON.

## Components

- `main.go` — zero-dependency Go server. File-backed session store under `data/`.
- `static/canvas.html` — TLDraw editor (loaded from esm.sh CDN, no build step).
- `static/index.html` — dashboard to create/list sessions.
- `canvas-cli` — bash helper an agent calls.

## API

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/canvas` | create `{prompt, by}` → `{id, url, status}` |
| GET  | `/api/canvas` | list sessions |
| GET  | `/api/canvas/{id}` | session meta (poll this) |
| POST | `/api/canvas/{id}/submit` | `{image(dataURL png), snapshot, text}` |
| GET  | `/api/canvas/{id}/image.png` | exported drawing |
| GET  | `/api/canvas/{id}/snapshot.json` | TLDraw document snapshot |
| GET  | `/c/{id}` | the canvas page (open on phone) |

## Access control (email-scoping)

Each session records an **owner** — the exe.dev email of whoever created it
(`X-ExeDev-Email`, set by the proxy for both the human's cookie session and the
agent's bearer token, which resolve to the same identity).

- A session **with an owner** is private: only that email may view it, draw on
  it, or read its image/snapshot. Others get `403`; the list endpoint hides it;
  an unauthenticated visitor to `/c/{id}` is bounced through exe.dev login.
- A session **without an owner** is unrestricted. This happens only when it was
  created with no identity — i.e. an agent hitting `http://localhost:8000` on
  the VM itself. That path is already private because the exe.dev proxy is
  private by default, so only the VM owner can reach it.

Upshot: when an **off-VM** agent creates a canvas with your bearer token, the
session is locked to your account — a leaked link is useless to anyone else.

## Install (register with an agent)

`./install` auto-detects VM vs laptop and wires up the chosen agent:

```sh
./install claude-code        # register MCP server with Claude Code
./install codex              # print ~/.codex/config.toml block
./install claude-desktop     # print claude_desktop_config.json block
./install cli                # symlink canvas-cli into ~/.local/bin
```

Off the VM it offers to mint a 30-day VM bearer token via
`ssh exe.dev ssh-key generate-api-key`.

## Run

```sh
go build -o canvas . && ./canvas        # listens on :8000
```

Or install the service:

```sh
sudo cp canvas.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now canvas
```

## CLI usage

```sh
./canvas-cli request "Sketch the homepage layout"   # create + wait + download
./canvas-cli create  "prompt..."                     # create only
./canvas-cli wait    <id> [out.png]                  # poll + download
./canvas-cli status  <id>
```

Env: `CANVAS_BASE` (agent-side, default `http://localhost:8000`),
`CANVAS_PUBLIC` (link shown to human, default `https://canvas.exe.xyz`),
`CANVAS_POLL` (seconds).
