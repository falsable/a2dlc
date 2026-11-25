// a2dlc native Go verification engine: implements diff, tier0, stub, signatures, deps_graph, and mutate
// over /io/request.json -> /io/response.json using Go's standard AST/parser/format packages and go vet/test.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

const (
	engineName    = "a2dlc-go"
	engineVersion = "0.1.0"
)

// ioDir is the folder of the request, response, stubs, and mutants. Overridden in tests.
var ioDir = "/io"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc-go:", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile(filepath.Join(ioDir, "request.json"))
	if err != nil {
		return err
	}
	var req contract.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return writeErrorResponse(req.Operation, fmt.Sprintf("invalid request: %v", err))
	}
	var resp any
	switch req.Operation {
	case "", "diff":
		resp = opDiff(req)
	case "tier0":
		resp = opTier0(req)
	case "stub":
		resp = opStub(req)
	case "signatures":
		resp = opSignatures(req)
	case "deps_graph":
		resp = opDepsGraph(req)
	case "mutate":
		resp = opMutate(req)
	default:
		return writeErrorResponse(req.Operation, fmt.Sprintf("unsupported operation %q", req.Operation))
	}
	return writeJSON(filepath.Join(ioDir, "response.json"), resp)
}

func writeErrorResponse(op, msg string) error {
	payload := map[string]any{
		"schema_version": contract.SchemaVersion,
		"error":          msg,
	}
	switch op {
	case "", "diff":
		payload["targets"] = []any{}
		payload["out_of_scope"] = []any{}
	case "tier0":
		payload["ok"] = false
		payload["findings"] = []any{}
	case "stub":
		payload["stubbed"] = []any{}
		payload["unstubbed"] = []any{}
	case "signatures":
		payload["files"] = []any{}
	case "deps_graph":
		payload["edges"] = []any{}
		payload["unresolved"] = []any{}
	case "mutate":
		payload["mutants"] = []any{}
		payload["not_generated"] = []any{}
		payload["pruned"] = []any{}
	}
	return writeJSON(filepath.Join(ioDir, "response.json"), payload)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func isGoFile(rel string) bool {
	return strings.HasSuffix(rel, ".go")
}

func isGoTestFile(rel string) bool {
	return strings.HasSuffix(rel, "_test.go")
}

func absUnder(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}
