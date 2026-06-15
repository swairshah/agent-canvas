// canvas-mcp is a stdio MCP server exposing the Agent Canvas as tools.
//
// It works in any MCP client that supports local stdio servers:
// Claude Desktop, Claude Code, and Codex CLI. (ChatGPT requires *remote* MCP
// and is web-only, so it is intentionally out of scope here.)
//
// Transport: newline-delimited JSON-RPC 2.0 over stdin/stdout (MCP stdio).
//
// Config (env):
//   CANVAS_BASE   canvas server base URL.
//                   on the VM:  http://localhost:8000 (default)
//                   off the VM: https://canvas.exe.xyz (also set CANVAS_TOKEN)
//   CANVAS_TOKEN  exe.dev VM bearer token (needed only off-VM)
//   CANVAS_PUBLIC public base shown to the human (default https://canvas.exe.xyz)
//   CANVAS_POLL   poll seconds for canvas_wait (default 3)
//
// Tools:
//   canvas_create {prompt}        -> {id, url} (link to open on phone)
//   canvas_status {id}            -> session metadata
//   canvas_wait   {id, timeout?}  -> blocks until done; returns the drawing as
//                                    an image content block the model can see
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	baseURL   = env("CANVAS_BASE", "http://localhost:8000")
	publicURL = env("CANVAS_PUBLIC", "https://canvas.exe.xyz")
	token     = os.Getenv("CANVAS_TOKEN")
	pollSecs  = envInt("CANVAS_POLL", 3)
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return d
}

// ---- minimal HTTP client against the canvas API ----

func doReq(method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Exedev-Authorization", "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

func getJSON(path string, out any) error {
	resp, err := doReq("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- JSON-RPC plumbing ----

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	out := json.NewEncoder(os.Stdout)

	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		resp, isNotification := handle(&req)
		if isNotification {
			continue // notifications get no reply
		}
		out.Encode(resp)
	}
}

func handle(req *rpcReq) (*rpcResp, bool) {
	var id any
	if len(req.ID) > 0 {
		json.Unmarshal(req.ID, &id)
	}
	ok := func(result any) *rpcResp {
		return &rpcResp{JSONRPC: "2.0", ID: id, Result: result}
	}
	fail := func(code int, msg string) *rpcResp {
		return &rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		return ok(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agent-canvas", "version": "0.1.0"},
		}), false
	case "notifications/initialized", "notifications/cancelled":
		return nil, true
	case "ping":
		return ok(map[string]any{}), false
	case "tools/list":
		return ok(map[string]any{"tools": toolList()}), false
	case "tools/call":
		return callTool(req.Params, ok, fail), false
	default:
		return fail(-32601, "method not found: "+req.Method), false
	}
}

func toolList() []any {
	str := map[string]any{"type": "string"}
	return []any{
		map[string]any{
			"name":        "canvas_create",
			"description": "Create a drawing canvas request for the human. Returns a URL the human opens on their phone (or any browser) to draw a response. Use when you need a sketch, diagram, layout, or hand-drawn input from the user.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"prompt": str},
				"required":   []string{"prompt"},
			},
		},
		map[string]any{
			"name":        "canvas_status",
			"description": "Check whether a canvas has been completed by the human. Returns status (pending|done) and metadata.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": str},
				"required":   []string{"id"},
			},
		},
		map[string]any{
			"name":        "canvas_wait",
			"description": "Block until the human submits the canvas, then return their drawing as an image you can see. Optional timeout in seconds (default 300).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      str,
					"timeout": map[string]any{"type": "number"},
				},
				"required": []string{"id"},
			},
		},
	}
}

type session struct {
	ID       string `json:"id"`
	Prompt   string `json:"prompt"`
	Status   string `json:"status"`
	Text     string `json:"text"`
	HasImage bool   `json:"has_image"`
}

func callTool(params json.RawMessage, ok func(any) *rpcResp, fail func(int, string) *rpcResp) *rpcResp {
	var p struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return fail(-32602, "bad params: "+err.Error())
	}
	argStr := func(k string) string {
		if v, isStr := p.Args[k].(string); isStr {
			return v
		}
		return ""
	}

	switch p.Name {
	case "canvas_create":
		prompt := argStr("prompt")
		resp, err := doReq("POST", "/api/canvas", map[string]string{"prompt": prompt, "by": "mcp"})
		if err != nil {
			return fail(-32000, err.Error())
		}
		defer resp.Body.Close()
		var r struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		json.NewDecoder(resp.Body).Decode(&r)
		link := publicURL + r.URL
		return ok(textResult(fmt.Sprintf("Canvas created.\nid: %s\nOpen this link to draw: %s\n\nThen call canvas_wait with this id to receive the drawing.", r.ID, link)))

	case "canvas_status":
		var s session
		if err := getJSON("/api/canvas/"+argStr("id"), &s); err != nil {
			return fail(-32000, err.Error())
		}
		b, _ := json.Marshal(s)
		return ok(textResult(string(b)))

	case "canvas_wait":
		id := argStr("id")
		timeout := 300.0
		if v, isNum := p.Args["timeout"].(float64); isNum && v > 0 {
			timeout = v
		}
		deadline := time.Now().Add(time.Duration(timeout) * time.Second)
		for {
			var s session
			if err := getJSON("/api/canvas/"+id, &s); err != nil {
				return fail(-32000, err.Error())
			}
			if s.Status == "done" {
				return ok(doneResult(id, s))
			}
			if time.Now().After(deadline) {
				return ok(textResult("Timed out waiting for the human to submit the canvas (still pending). Call canvas_wait again to keep waiting."))
			}
			time.Sleep(time.Duration(pollSecs) * time.Second)
		}

	default:
		return fail(-32601, "unknown tool: "+p.Name)
	}
}

func textResult(s string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
}

// doneResult returns a text note plus the drawing as an image content block so
// the model can actually see what was drawn.
func doneResult(id string, s session) map[string]any {
	content := []any{}
	note := "The human submitted the canvas."
	if s.Text != "" {
		note += " Note: " + s.Text
	}
	content = append(content, map[string]any{"type": "text", "text": note})

	if s.HasImage {
		if resp, err := doReq("GET", "/api/canvas/"+id+"/image.png", nil); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				if raw, err := io.ReadAll(resp.Body); err == nil {
					content = append(content, map[string]any{
						"type":     "image",
						"data":     base64.StdEncoding.EncodeToString(raw),
						"mimeType": "image/png",
					})
				}
			}
		}
	}
	return map[string]any{"content": content}
}
