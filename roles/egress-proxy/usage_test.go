package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyUsageAccountingJSONAndSSEAndEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":25,\"cost\":0.004}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":50,"output_tokens":10,"cache_read_input_tokens":5,"cost":0.002}}`))
	}))
	defer upstream.Close()

	allow, err := ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := ParseRoutes(`{"llm":{"target":"`+upstream.URL+`","allow_http":true}}`, allow)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(allow, routes)
	srv := httptest.NewServer(p)
	defer srv.Close()

	// 1. JSON call
	resp1, err := http.Post(srv.URL+"/r/llm/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()

	// 2. SSE call
	resp2, err := http.Post(srv.URL+"/r/llm/stream", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	// 3. Query /__a2dlc/usage
	uResp, err := http.Get(srv.URL + "/__a2dlc/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer uResp.Body.Close()
	var got UsageSummary
	if err := json.NewDecoder(uResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Calls != 2 || got.InputTokens != 150 || got.OutputTokens != 35 || got.CacheReadInputTokens != 5 {
		t.Fatalf("usage = %+v", got)
	}
	if got.USD < 0.0059 || got.USD > 0.0061 {
		t.Fatalf("USD = %v, want ~0.006", got.USD)
	}
}

// proxyUsage sends one POST through a proxy whose upstream answers with body,
// then returns the raw /__a2dlc/usage document.
func proxyUsage(t *testing.T, contentType, body string) map[string]any {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()
	allow, err := ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := ParseRoutes(`{"llm":{"target":"`+upstream.URL+`","allow_http":true}}`, allow)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewProxy(allow, routes))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/r/llm/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	uResp, err := http.Get(srv.URL + "/__a2dlc/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer uResp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(uResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProxyUsageLongJSONCompletionKeepsTrailingUsage(t *testing.T) {
	// Providers put `usage` after the content, so a 300 KB completion pushes it past the head buffer.
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("x", 300*1024) +
		`"}}],"usage":{"prompt_tokens":120,"completion_tokens":75000,"cost":0.40,"prompt_tokens_details":{"cached_tokens":3}}}`
	got := proxyUsage(t, "application/json", body)
	if got["calls"] != float64(1) || got["input_tokens"] != float64(120) || got["output_tokens"] != float64(75000) {
		t.Fatalf("usage = %v", got)
	}
	if usd, _ := got["usd"].(float64); usd < 0.3999 || usd > 0.4001 {
		t.Fatalf("USD = %v, want 0.40", got["usd"])
	}
	if got["unparsed"] != float64(0) {
		t.Fatalf("unparsed = %v, want 0", got["unparsed"])
	}
}

func TestProxyUsageIgnoresUsageLookalikeInsideContent(t *testing.T) {
	// The only real usage is the trailing one; the escaped text inside the content must not win.
	fake := `\"usage\":{\"prompt_tokens\":9999,\"cost\":9}`
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("y", 300*1024) + fake +
		`"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":0.01}}`
	got := proxyUsage(t, "application/json", body)
	if got["input_tokens"] != float64(1) || got["output_tokens"] != float64(2) {
		t.Fatalf("usage = %v", got)
	}
}

func TestProxyUsageCountsResponsesWithoutParsableUsage(t *testing.T) {
	cases := map[string]string{
		"not json":                 "upstream exploded",
		"long body without usage":  `{"choices":[{"message":{"content":"` + strings.Repeat("z", 300*1024) + `"}}]}`,
		"short body without usage": `{"error":{"message":"rate limited"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := proxyUsage(t, "application/json", body)
			if got["calls"] != float64(1) || got["unparsed"] != float64(1) {
				t.Fatalf("usage = %v, want calls=1 unparsed=1", got)
			}
			if got["usd"] != float64(0) {
				t.Fatalf("usd = %v, want 0", got["usd"])
			}
		})
	}
}
