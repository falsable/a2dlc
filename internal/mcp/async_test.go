package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
)

func TestMCPServeProgressAndCancelledNotification(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), app.App{Version: "0.3.0-test"}, pr, &out)
	}()

	// 1. Send a tools/call with _meta.progressToken
	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"a2dlc_status","arguments":{},"_meta":{"progressToken":"tok-1"}}}`+"\n")
	// 2. Send a notifications/cancelled for a synthetic id (verifies notification parsing doesn't block or error)
	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":10,"reason":"user_abort"}}`+"\n")
	// 3. Send a ping right after
	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":11,"method":"ping"}`+"\n")
	_ = pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve timed out")
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var sawProgressStart, sawProgressEnd, sawCallResp, sawPing bool
	for _, l := range lines {
		var msg map[string]any
		if json.Unmarshal([]byte(l), &msg) != nil {
			t.Fatalf("invalid json output line: %s", l)
		}
		if m, _ := msg["method"].(string); m == "notifications/progress" {
			params, _ := msg["params"].(map[string]any)
			if params["progressToken"] == "tok-1" {
				if p, _ := params["progress"].(float64); p == 0 {
					sawProgressStart = true
				} else if p == 1 {
					sawProgressEnd = true
				}
			}
		}
		if id, ok := msg["id"].(float64); ok {
			if id == 10 {
				sawCallResp = true
			} else if id == 11 {
				sawPing = true
			}
		}
	}
	if !sawProgressStart || !sawProgressEnd || !sawCallResp || !sawPing {
		t.Fatalf("missing expected messages (start=%v end=%v call=%v ping=%v) in:\n%s",
			sawProgressStart, sawProgressEnd, sawCallResp, sawPing, out.String())
	}
}
