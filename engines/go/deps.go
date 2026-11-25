package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

func opDepsGraph(req contract.Request) contract.DepsResponse {
	resp := contract.DepsResponse{
		SchemaVersion: contract.SchemaVersion,
		Edges:         []contract.DepsEdge{},
		Unresolved:    []string{},
	}
	modPath := readGoModPath(filepath.Join(req.CandidateDir, "go.mod"))
	pkgFiles := map[string][]string{}
	for _, rel := range req.ChangedFiles {
		if !isGoFile(rel) || isGoTestFile(rel) {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		pkgFiles[dir] = append(pkgFiles[dir], rel)
	}

	fset := token.NewFileSet()
	seenEdge := map[string]bool{}
	seenUnres := map[string]bool{}
	for _, rel := range req.ChangedFiles {
		if !isGoFile(rel) || isGoTestFile(rel) {
			continue
		}
		abs := absUnder(req.CandidateDir, rel)
		file, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if modPath == "" {
				continue
			}
			if p == modPath || strings.HasPrefix(p, modPath+"/") {
				sub := strings.TrimPrefix(strings.TrimPrefix(p, modPath), "/")
				targets := pkgFiles[sub]
				if len(targets) == 0 {
					// Check if sub exists in repo (outside changed_files) vs unresolved.
					if _, statErr := os.Stat(filepath.Join(req.CandidateDir, filepath.FromSlash(sub))); statErr != nil {
						if !seenUnres[p] {
							seenUnres[p] = true
							resp.Unresolved = append(resp.Unresolved, p)
						}
					}
					continue
				}
				for _, dst := range targets {
					if dst == rel {
						continue
					}
					key := rel + "->" + dst
					if !seenEdge[key] {
						seenEdge[key] = true
						resp.Edges = append(resp.Edges, contract.DepsEdge{From: rel, To: dst})
					}
				}
			}
		}
	}
	sort.Strings(resp.Unresolved)
	return resp
}

func readGoModPath(goModPath string) string {
	b, err := os.ReadFile(goModPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}
