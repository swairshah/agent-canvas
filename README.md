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
