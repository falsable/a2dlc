package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// HTTPStep is one HTTP request in a stateful differential sequence.
type HTTPStep struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// CompareHTTPSequences executes stateful HTTP request sequences against baseURL and candURL
// and returns PASS if all responses match, or FAIL with the first diverging Counterexample.
func CompareHTTPSequences(ctx context.Context, baseURL, candURL string, sequences [][]HTTPStep) (string, *contract.Counterexample, error) {
	baseJar, _ := cookiejar.New(nil)
	candJar, _ := cookiejar.New(nil)
	baseClient := &http.Client{Timeout: 5 * time.Second, Jar: baseJar}
	candClient := &http.Client{Timeout: 5 * time.Second, Jar: candJar}

	baseURL = strings.TrimRight(baseURL, "/")
	candURL = strings.TrimRight(candURL, "/")

	for _, seq := range sequences {
		for stepIdx, step := range seq {
			oldStatus, oldBody, err := doHTTPStep(ctx, baseClient, baseURL, step)
			if err != nil {
				return "", nil, fmt.Errorf("base step %d (%s %s): %w", stepIdx, step.Method, step.Path, err)
			}
			newStatus, newBody, err := doHTTPStep(ctx, candClient, candURL, step)
			if err != nil {
				return "", nil, fmt.Errorf("cand step %d (%s %s): %w", stepIdx, step.Method, step.Path, err)
			}
			if oldStatus != newStatus || normalizeHTTPBody(oldBody) != normalizeHTTPBody(newBody) {
				argsJSON, _ := json.Marshal(seq[:stepIdx+1])
				oldJSON, _ := json.Marshal(map[string]any{"status": oldStatus, "body": normalizeHTTPBody(oldBody)})
				newJSON, _ := json.Marshal(map[string]any{"status": newStatus, "body": normalizeHTTPBody(newBody)})
				return contract.FAIL, &contract.Counterexample{
					Args: string(argsJSON),
					Old:  string(oldJSON),
					New:  string(newJSON),
				}, nil
			}
		}
	}
	return contract.PASS, nil, nil
}

func doHTTPStep(ctx context.Context, client *http.Client, baseURL string, step HTTPStep) (int, string, error) {
	method := step.Method
	if method == "" {
		method = http.MethodGet
	}
	path := step.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var bodyReader io.Reader
	if step.Body != "" {
		bodyReader = strings.NewReader(step.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bodyReader)
	if err != nil {
		return 0, "", err
	}
	for k, v := range step.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(b), nil
}

func normalizeHTTPBody(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(trimmed), &v) == nil {
		var buf bytes.Buffer
		if json.NewEncoder(&buf).Encode(v) == nil {
			return strings.TrimSpace(buf.String())
		}
	}
	return trimmed
}
