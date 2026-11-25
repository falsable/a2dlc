package run

import (
	"context"
	"os/exec"
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
