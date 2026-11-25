package run

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// StreamSession wraps a running bidirectional JSON-lines / ACP JSON-RPC role process.
// Callers receive live typed contract.StreamEvent values and can Steer or Cancel mid-flight.
type StreamSession struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	mu     sync.Mutex
	closed bool

	events chan contract.StreamEvent
	done   chan error

	evMu   sync.Mutex
	logBuf strings.Builder
	allEvs []contract.StreamEvent
}

// StartStreamCmd starts cmd with bidirectional stdin/stdout JSON-lines/ACP streaming.
// Optional onEvent is invoked synchronously for each decoded event before it is queued.
func StartStreamCmd(ctx context.Context, cmd *exec.Cmd, onEvent func(contract.StreamEvent)) (*StreamSession, error) {
	sCtx, cancel := context.WithCancel(ctx)
	// Rebind command context if not already bound, preserving Path/Args/Env/Dir.
	bound := exec.CommandContext(sCtx, cmd.Path, cmd.Args[1:]...)
	bound.Env = cmd.Env
	bound.Dir = cmd.Dir

	stdin, err := bound.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := bound.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	var stderr strings.Builder
	bound.Stderr = &stderr

	if err := bound.Start(); err != nil {
		cancel()
		return nil, err
	}

	sess := &StreamSession{
		cmd:    bound,
		cancel: cancel,
		stdin:  stdin,
		events: make(chan contract.StreamEvent, 64),
		done:   make(chan error, 1),
	}

	go func() {
		defer close(sess.events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		seq := 0
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			seq++
			ev := decodeStreamLine(line, seq)
			sess.evMu.Lock()
			sess.logBuf.WriteString(line)
			sess.logBuf.WriteByte('\n')
			sess.allEvs = append(sess.allEvs, ev)
			sess.evMu.Unlock()
			if onEvent != nil {
				onEvent(ev)
			}
			sess.events <- ev
		}
		waitErr := bound.Wait()
		if waitErr != nil && stderr.Len() > 0 {
			waitErr = fmt.Errorf("%w: %s", waitErr, strings.TrimSpace(stderr.String()))
		}
		sess.done <- waitErr
		close(sess.done)
	}()

	return sess, nil
}

// Events returns the channel of typed stream events (closed when stdout reaches EOF).
func (s *StreamSession) Events() <-chan contract.StreamEvent {
	return s.events
}

// Wait waits for the streamed process to exit and returns its exit error, if any.
func (s *StreamSession) Wait() error {
	return <-s.done
}

// CollectedEvents returns a snapshot of all typed events received so far.
func (s *StreamSession) CollectedEvents() []contract.StreamEvent {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	out := make([]contract.StreamEvent, len(s.allEvs))
	copy(out, s.allEvs)
	return out
}

// Steer sends a mid-flight steering directive over stdin to the running role.
func (s *StreamSession) Steer(message string) error {
	return s.sendCommand(contract.StreamCommand{
		JSONRPC: "2.0",
		Method:  "session/steer",
		Type:    "steer",
		Message: message,
	})
}

// Cancel sends a mid-flight cancellation notification over stdin and cancels the process context.
func (s *StreamSession) Cancel(reason string) error {
	err := s.sendCommand(contract.StreamCommand{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Type:    "cancel",
		Reason:  reason,
	})
	s.cancel()
	return err
}

func (s *StreamSession) sendCommand(cmd contract.StreamCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("stream session closed")
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func decodeStreamLine(line string, seq int) contract.StreamEvent {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Try direct contract.StreamEvent first.
	var ev contract.StreamEvent
	if json.Unmarshal([]byte(line), &ev) == nil && ev.Type != "" {
		if ev.SchemaVersion == 0 {
			ev.SchemaVersion = contract.SchemaVersion
		}
		if ev.Seq == 0 {
			ev.Seq = seq
		}
		if ev.Timestamp == "" {
			ev.Timestamp = now
		}
		return ev
	}
	// Try ACP / JSON-RPC notification (e.g. {"jsonrpc":"2.0","method":"session/update","params":{...}}).
	var rpc struct {
		Method string `json:"method"`
		Params struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Tool  string         `json:"tool"`
			Usage *contract.Cost `json:"usage"`
			Error string         `json:"error"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(line), &rpc) == nil && rpc.Method != "" {
		t := contract.StreamEventType(rpc.Params.Type)
		if t == "" {
			t = contract.StreamText
		}
		return contract.StreamEvent{
			SchemaVersion: contract.SchemaVersion,
			Seq:           seq,
			Timestamp:     now,
			Type:          t,
			Text:          rpc.Params.Text,
			Tool:          rpc.Params.Tool,
			Usage:         rpc.Params.Usage,
			Error:         rpc.Params.Error,
		}
	}
	return contract.StreamEvent{
		SchemaVersion: contract.SchemaVersion,
		Seq:           seq,
		Timestamp:     now,
		Type:          contract.StreamText,
		Text:          line,
	}
}
