package main

import (
	"encoding/json"
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
