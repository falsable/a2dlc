package mcp

import (
	"bufio"
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

// A slow tools/call must not hold up ping or the other calls, and notifications/cancelled still reaches a call that is
// running beside it.
func TestMCPServeSlowToolCallDoesNotBlockPingOrCancel(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 2)
	old := dispatch
	dispatch = func(ctx context.Context, _ app.App, p callToolParams) callToolResult {
		started <- p.Name
		select {
		case <-release:
			return callToolResult{Content: []contentBlock{{Type: "text", Text: "slow done"}}}
		case <-ctx.Done():
			return callToolResult{Content: []contentBlock{{Type: "text", Text: "cancelled"}}, IsError: true}
		}
	}
	defer func() { dispatch = old }()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		err := Serve(context.Background(), app.App{}, inR, outW)
		outW.Close()
		served <- err
	}()
	got := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				got <- m
			}
		}
		close(got)
	}()
	next := func(what string) map[string]any {
		t.Helper()
		select {
		case m, ok := <-got:
			if !ok {
				t.Fatalf("output closed while waiting for %s", what)
			}
			return m
		case <-time.After(3 * time.Second):
			t.Fatalf("no response for %s: the slow call is blocking the server", what)
		}
		return nil
	}

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{}}}`+"\n")
	<-started
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	if m := next("ping while a call runs"); m["id"] != float64(2) {
		t.Fatalf("first response is %v, want the ping (id 2)", m)
	}
	// a second call starts while the first still runs, and is cancelled on its own
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow2","arguments":{}}}`+"\n")
	<-started
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`+"\n")
	if m := next("the cancelled call"); m["id"] != float64(3) {
		t.Fatalf("response is %v, want the cancelled call (id 3)", m)
	}
	close(release)
	if m := next("the slow call"); m["id"] != float64(1) {
		t.Fatalf("response is %v, want the slow call (id 1)", m)
	}
	_ = inW.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
}
