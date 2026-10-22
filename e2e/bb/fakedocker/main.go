// fakedocker stands in for `docker` inside the black-box engine's container, where there is no Docker: the core
// finds it through PATH. It reads the request.json of the mounted /io and answers according to $A2DLC_WORK/fake.json
// and the mode that setup encoded in the image name (fake-<ok|crash|basura>-<i>). It imports nothing from the module.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	args := os.Args[1:]
	io, image := "", ""
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasSuffix(args[i+1], ":/io") {
			io = strings.TrimSuffix(args[i+1], ":/io")
		}
	}
	if len(args) > 0 {
		image = args[len(args)-1]
	}
	if io == "" {
		fmt.Fprintln(os.Stderr, "fakedocker: no -v <dir>:/io")
		os.Exit(125)
	}
	switch {
	case strings.HasPrefix(image, "fake-crash-"):
		fmt.Fprintln(os.Stderr, "fakedocker: engine crashed on purpose")
		os.Exit(1)
	case strings.HasPrefix(image, "fake-basura-"):
		_ = os.WriteFile(filepath.Join(io, "response.json"), []byte("{"), 0o644)
		return
	}
	var req struct {
		ChangedFiles []string `json:"changed_files"`
	}
	raw, err := os.ReadFile(filepath.Join(io, "request.json"))
	if err == nil {
		err = json.Unmarshal(raw, &req)
	}
	var fake struct {
		Verdicts map[string]string `json:"verdicts"`
	}
	if err == nil {
		raw, err = os.ReadFile(filepath.Join(os.Getenv("A2DLC_WORK"), "fake.json"))
		if err == nil {
			err = json.Unmarshal(raw, &fake)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakedocker:", err)
		os.Exit(125)
	}
	targets := []any{}
	for _, f := range req.ChangedFiles {
		v := fake.Verdicts[f]
		if v == "" {
			v = "PASS"
		}
		t := map[string]any{"target": f + "::f", "engine": "fast-check", "engine_version": "fake",
			"verdict": v, "search_budget": map[string]any{"examples": 1, "seed": 1}, "generators": "typed_default",
			"typed": true, "domain_guard": nil, "equivalence": "strict", "counterexample": nil}
		switch v {
		case "FAIL":
			t["counterexample"] = map[string]any{"args": "[]", "old": "1", "new": "2"}
		case "INCONCLUSIVE":
			t["reason"] = "fake"
		}
		targets = append(targets, t)
	}
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "targets": targets, "out_of_scope": []any{}, "error": nil})
	if err := os.WriteFile(filepath.Join(io, "response.json"), b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fakedocker:", err)
		os.Exit(125)
	}
}
