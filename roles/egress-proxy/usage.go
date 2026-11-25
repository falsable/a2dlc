package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// UsageSummary holds proxy-verified LLM call, token, and USD spend counters.
type UsageSummary struct {
	Calls                    int     `json:"calls"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	USD                      float64 `json:"usd"`
	// Unparsed counts non-streamed responses from which no usage could be read: their spend is missing from the totals.
	Unparsed int `json:"unparsed"`
}

type usageTracker struct {
	mu      sync.Mutex
	summary UsageSummary
}

func (u *usageTracker) recordCall(resp *http.Response) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.summary.Calls++
	if resp != nil {
		if raw := strings.TrimSpace(resp.Header.Get("X-OpenRouter-Cost")); raw != "" {
			if usd, err := strconv.ParseFloat(raw, 64); err == nil && usd > 0 {
				u.summary.USD += usd
			}
		}
	}
}

func (u *usageTracker) recordUnparsed() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.summary.Unparsed++
}

// recordPayload adds the usage found in one JSON document and reports whether it found any.
func (u *usageTracker) recordPayload(b []byte) bool {
	var parsed struct {
		Usage *struct {
			InputTokens              int     `json:"input_tokens"`
			OutputTokens             int     `json:"output_tokens"`
			PromptTokens             int     `json:"prompt_tokens"`
			CompletionTokens         int     `json:"completion_tokens"`
			CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
			Cost                     float64 `json:"cost"`
		} `json:"usage"`
		Message *struct {
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &parsed) != nil {
		return false
	}
	found := parsed.Usage != nil || (parsed.Message != nil && parsed.Message.Usage != nil)
	u.mu.Lock()
	defer u.mu.Unlock()
	if parsed.Usage != nil {
		inTok := parsed.Usage.InputTokens
		if inTok == 0 {
			inTok = parsed.Usage.PromptTokens
		}
		outTok := parsed.Usage.OutputTokens
		if outTok == 0 {
			outTok = parsed.Usage.CompletionTokens
		}
		u.summary.InputTokens += inTok
		u.summary.OutputTokens += outTok
		u.summary.CacheReadInputTokens += parsed.Usage.CacheReadInputTokens
		u.summary.CacheCreationInputTokens += parsed.Usage.CacheCreationInputTokens
		if parsed.Usage.Cost > 0 {
			u.summary.USD += parsed.Usage.Cost
		}
	}
	if parsed.Message != nil && parsed.Message.Usage != nil {
		u.summary.InputTokens += parsed.Message.Usage.InputTokens
		u.summary.OutputTokens += parsed.Message.Usage.OutputTokens
		u.summary.CacheReadInputTokens += parsed.Message.Usage.CacheReadInputTokens
		u.summary.CacheCreationInputTokens += parsed.Message.Usage.CacheCreationInputTokens
	}
	return found
}

func (u *usageTracker) snapshot() UsageSummary {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.summary
}

// Usage returns the proxy-verified token and USD usage recorded so far.
func (p *Proxy) Usage() UsageSummary {
	if p.usage == nil {
		return UsageSummary{}
	}
	return p.usage.snapshot()
}

type streamUsageInspector struct {
	tracker *usageTracker
	isSSE   bool
	buf     bytes.Buffer
	tail    []byte
	total   int
	lineBuf bytes.Buffer
}

func (s *streamUsageInspector) write(chunk []byte) {
	if s.tracker == nil || len(chunk) == 0 {
		return
	}
	if s.isSSE {
		s.lineBuf.Write(chunk)
		for {
			b := s.lineBuf.Bytes()
			idx := bytes.IndexByte(b, '\n')
			if idx < 0 {
				break
			}
			line := strings.TrimSpace(string(b[:idx]))
			s.lineBuf.Next(idx + 1)
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload != "" && payload != "[DONE]" {
					s.tracker.recordPayload([]byte(payload))
				}
			}
		}
		return
	}
	s.total += len(chunk)
	if s.buf.Len() < maxHeadBuf {
		rem := maxHeadBuf - s.buf.Len()
		if len(chunk) > rem {
			s.buf.Write(chunk[:rem])
		} else {
			s.buf.Write(chunk)
		}
	}
	// Keep the last maxTailBuf bytes of the whole body: providers put `usage` after the content.
	s.tail = append(s.tail, chunk...)
	if len(s.tail) > 2*maxTailBuf {
		s.tail = append(s.tail[:0], s.tail[len(s.tail)-maxTailBuf:]...)
	}
}

// Non-streamed bodies are held as a head (parsed whole when the body fits in it) and a tail window.
const (
	maxHeadBuf = 256 * 1024
	maxTailBuf = 64 * 1024
)

// trailingUsage returns the last `"usage":{...}` object of a JSON body tail as a one-key document.
// An unescaped `"usage"` can only be an object key: inside strings the quotes are escaped.
func trailingUsage(tail []byte) []byte {
	key := []byte(`"usage"`)
	for end := len(tail); end > 0; {
		i := bytes.LastIndex(tail[:end], key)
		if i < 0 {
			return nil
		}
		end = i
		rest := bytes.TrimLeft(tail[i+len(key):], " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue
		}
		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		if len(rest) == 0 || rest[0] != '{' {
			continue
		}
		var obj json.RawMessage
		if json.NewDecoder(bytes.NewReader(rest)).Decode(&obj) != nil {
			continue
		}
		return append(append([]byte(`{"usage":`), obj...), '}')
	}
	return nil
}

func (s *streamUsageInspector) finish() {
	if s.tracker == nil {
		return
	}
	if s.isSSE {
		if rem := strings.TrimSpace(s.lineBuf.String()); strings.HasPrefix(rem, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(rem, "data:"))
			if payload != "" && payload != "[DONE]" {
				s.tracker.recordPayload([]byte(payload))
			}
		}
		return
	}
	if s.total == 0 {
		return
	}
	var found bool
	if s.total <= maxHeadBuf {
		found = s.tracker.recordPayload(s.buf.Bytes())
	} else if doc := trailingUsage(s.tail); doc != nil {
		found = s.tracker.recordPayload(doc)
	}
	if !found {
		s.tracker.recordUnparsed()
	}
}
