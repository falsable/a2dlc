package run

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestStreamSessionLiveEventsAndSteerAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use a small inline sh script to simulate an ACP/JSON-lines role process reading stdin commands.
	script := `
printf '{"schema_version":1,"type":"started","role":"builder"}\n'
printf '{"jsonrpc":"2.0","method":"session/update","params":{"type":"tool_use","tool":"read_file"}}\n'
IFS= read -r cmd
case "$cmd" in
  *"focus on edge cases"*) printf '{"schema_version":1,"type":"steering","text":"ack"}\n' ;;
esac
printf '{"schema_version":1,"type":"completed"}\n'
`
	cmd := exec.Command("sh", "-c", script)
	var seen []contract.StreamEvent
	sess, err := StartStreamCmd(ctx, cmd, func(ev contract.StreamEvent) {
		seen = append(seen, ev)
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := sess.Steer("focus on edge cases"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Drain channel
	for range sess.Events() {
	}

	evs := sess.CollectedEvents()
	if len(evs) != 4 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	if evs[0].Type != contract.StreamStarted || evs[1].Type != contract.StreamToolUse || evs[1].Tool != "read_file" ||
		evs[2].Type != contract.StreamSteering || evs[3].Type != contract.StreamCompleted {
		t.Fatalf("unexpected events: %+v", evs)
	}
}

// A caller that Waits before it reads Events must not hang the reader once the 64-event buffer is full; the events stay
// available to a later reader, Events closes at EOF, and Steer after the process exited says the session is closed.
func TestStreamSessionWaitDoesNotNeedEventsDrained(t *testing.T) {
	const n = 70 // above the 64-event buffer
	cmd := exec.Command("sh", "-c", `i=0; while [ $i -lt 70 ]; do echo "line $i"; i=$((i+1)); done`)
	sess, err := StartStreamCmd(context.Background(), cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return: the reader is blocked on an undrained Events channel")
	}
	if got := len(sess.CollectedEvents()); got != n {
		t.Fatalf("collected %d events, want %d", got, n)
	}
	if err := sess.Steer("late"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Steer after exit: %v, want a closed-session error", err)
	}
	got := 0
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case _, ok := <-sess.Events():
			if !ok {
				done = true
			} else {
				got++
			}
		case <-timeout:
			t.Fatalf("Events not closed after %d events", got)
		}
	}
	if got != n {
		t.Fatalf("Events delivered %d, want %d", got, n)
	}
}
