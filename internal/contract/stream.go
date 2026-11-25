package contract

// StreamEventType enumerates the typed live events emitted by a streaming role transport.
type StreamEventType string

const (
	StreamStarted    StreamEventType = "started"
	StreamText       StreamEventType = "text"
	StreamToolUse    StreamEventType = "tool_use"
	StreamToolResult StreamEventType = "tool_result"
	StreamUsage      StreamEventType = "usage"
	StreamSteering   StreamEventType = "steering"
	StreamCancelled  StreamEventType = "cancelled"
	StreamCompleted  StreamEventType = "completed"
	StreamError      StreamEventType = "error"
)

// StreamEvent is one typed event emitted on a bidirectional role stream (ACP/JSON-RPC or JSON-lines).
type StreamEvent struct {
	SchemaVersion int             `json:"schema_version"`
	Seq           int             `json:"seq"`
	Timestamp     string          `json:"timestamp,omitempty"`
	Type          StreamEventType `json:"type"`
	Role          string          `json:"role,omitempty"`
	Stage         string          `json:"stage,omitempty"`
	Text          string          `json:"text,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Usage         *Cost           `json:"usage,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// StreamCommand is a mid-flight control message sent from the orchestrator to a running role.
type StreamCommand struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	Method  string `json:"method,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
