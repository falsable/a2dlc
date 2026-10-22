// Package graph finds the blast-radius edges that live inside one repository (integration.json.discovered): from a
// changed OpenAPI or protobuf contract to the generated clients that carry its names, and from a changed module to the
// files that import it (the engines answer the imports; the app layer converts them with ImportEdges). It works on the
// contents it is given: no git, no engine, no glob matching.
//
// Direction follows contracts/examples/valid/integration__discovered.json: an import edge goes from the importer to
// the imported file, a contract edge from the contract to its client.
package graph

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// The vias of a discovered edge (integration.schema.json).
const (
	ViaImport    = "import"
	ViaOpenAPI   = "openapi"
	ViaProtobuf  = "protobuf"
	ViaCodeGraph = "code_graph"
)

// minName is the shortest name a client is matched by: shorter words (api, id, v1) appear in almost any file.
const minName = 3

// Contract is a contract file of the tree: its kind (ViaOpenAPI or ViaProtobuf) and the names its clients carry.
type Contract struct {
	Via   string
	Names []string
}

// ContractOf says whether file, with its content, is a contract. An OpenAPI document is a .json file with a top-level
// openapi or swagger key, or a .yaml or .yml file with one of them at column 0. A .proto file is a contract when it
// declares a service or a message. The names are the static segments of the OpenAPI paths (a segment with a template
// such as {id} is not static) and the service and message names of a .proto file.
func ContractOf(file string, content []byte) (Contract, bool) {
	switch path.Ext(file) {
	case ".proto":
		return protoContract(content)
	case ".json":
		return openAPIJSON(content)
	case ".yaml", ".yml":
		return openAPIYAML(content)
	}
	return Contract{}, false
}

func openAPIJSON(content []byte) (Contract, bool) {
	var doc struct {
		OpenAPI json.RawMessage            `json:"openapi"`
		Swagger json.RawMessage            `json:"swagger"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if json.Unmarshal(content, &doc) != nil || doc.OpenAPI == nil && doc.Swagger == nil {
		return Contract{}, false
	}
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	return Contract{Via: ViaOpenAPI, Names: segments(paths)}, true
}

var (
	yamlRootRe    = regexp.MustCompile(`^(openapi|swagger)\s*:`)
	yamlPathsRe   = regexp.MustCompile(`^paths\s*:\s*(#.*)?$`)
	yamlPathKeyRe = regexp.MustCompile(`^  (["']?/[^"':\s]*["']?)\s*:\s*(#.*)?$`)
)

// openAPIYAML reads the path keys of a YAML OpenAPI document with a line scanner: the keys of the paths block are the
// two-space indented ones that start with a slash. It is not a YAML parser, and it does not need to be: a path key that
// is written any other way is missed, which only narrows the edges.
func openAPIYAML(content []byte) (Contract, bool) {
	root, inPaths := false, false
	var paths []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case yamlRootRe.MatchString(line):
			root, inPaths = true, false
		case yamlPathsRe.MatchString(line):
			inPaths = true
		case line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inPaths = false
		case inPaths:
			if m := yamlPathKeyRe.FindStringSubmatch(line); m != nil {
				paths = append(paths, strings.Trim(m[1], `"'`))
			}
		}
	}
	if !root {
		return Contract{}, false
	}
	return Contract{Via: ViaOpenAPI, Names: segments(paths)}, true
}

var protoNameRe = regexp.MustCompile(`(?m)^\s*(service|message)\s+([A-Za-z_]\w*)\s*\{`)

func protoContract(content []byte) (Contract, bool) {
	var names []string
	for _, m := range protoNameRe.FindAllStringSubmatch(string(content), -1) {
		if len(m[2]) >= minName {
			names = append(names, m[2])
		}
	}
	if len(names) == 0 {
		return Contract{}, false
	}
	return Contract{Via: ViaProtobuf, Names: unique(names)}, true
}

// segments returns the static path segments of paths that are long enough to name a client: sorted and unique.
func segments(paths []string) []string {
	var names []string
	for _, p := range paths {
		for _, s := range strings.Split(p, "/") {
			if len(s) >= minName && !strings.ContainsAny(s, "{}$") {
				names = append(names, s)
			}
		}
	}
	return unique(names)
}

// identRe is what a client's words are made of: a name matches a client when it appears as one whole word.
var identRe = regexp.MustCompile(`[A-Za-z0-9_-]+`)

// ContractEdges links each contract to each client that carries one of its names as a whole word. The edge goes from
// the contract to the client, via the kind of the contract. The caller passes the changed contracts (blast radius:
// a change reaches the clients of the contract it changed) and the files that match config.integration.generated_clients.
// Clients that are the same file as the contract are not linked to it.
func ContractEdges(contracts map[string]Contract, clients map[string][]byte) []contract.DiscoveredEdge {
	var edges []contract.DiscoveredEdge
	for _, client := range sortedKeys(clients) {
		words := map[string]bool{}
		for _, w := range identRe.FindAllString(string(clients[client]), -1) {
			words[w] = true
		}
		for _, from := range sortedKeys(contracts) {
			if from == client {
				continue
			}
			for _, name := range contracts[from].Names {
				if words[name] {
					edges = append(edges, contract.DiscoveredEdge{From: from, To: client, Via: contracts[from].Via})
					break
				}
			}
		}
	}
	return Normalize(edges)
}

// ImportEdges keeps the import edges that reach a changed file, the edges whose imported file (To) changed, and gives
// them the import via. Self imports and edges with an empty end (the schema asks for minLength 1) are dropped.
func ImportEdges(edges []contract.DepsEdge, changed map[string]bool) []contract.DiscoveredEdge {
	var out []contract.DiscoveredEdge
	for _, e := range edges {
		if e.From != "" && e.To != "" && e.From != e.To && changed[e.To] {
			out = append(out, contract.DiscoveredEdge{From: e.From, To: e.To, Via: ViaImport})
		}
	}
	return Normalize(out)
}

// CodeGraphEdges keeps the edges discovered by the Code Graph Indexer (ChronosGraph) that reach a changed file,
// marking them with ViaCodeGraph. The edge runs from the dependent file (From) to the changed file (To).
func CodeGraphEdges(edges []contract.DepsEdge, changed map[string]bool) []contract.DiscoveredEdge {
	var out []contract.DiscoveredEdge
	for _, e := range edges {
		if e.From != "" && e.To != "" && e.From != e.To && changed[e.To] {
			out = append(out, contract.DiscoveredEdge{From: e.From, To: e.To, Via: ViaCodeGraph})
		}
	}
	return Normalize(out)
}

// Reach returns the changed files and every file that reaches one of them through import edges (its importers, and the
// importers of those): the blast radius of the change inside the repo. Edges are read as the engine gives them, From
// importing To.
func Reach(edges []contract.DepsEdge, changed map[string]bool) map[string]bool {
	importers := map[string][]string{}
	for _, e := range edges {
		if e.From != "" && e.To != "" && e.From != e.To {
			importers[e.To] = append(importers[e.To], e.From)
		}
	}
	reach := map[string]bool{}
	var queue []string
	for f := range changed {
		reach[f] = true
		queue = append(queue, f)
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		for _, from := range importers[f] {
			if !reach[from] {
				reach[from] = true
				queue = append(queue, from)
			}
		}
	}
	return reach
}

// UnresolvedIn keeps, as the engine reported them, the unresolved imports ("<file>: <name>") whose importing file is in
// reach. The file is what comes before the first ": "; a repo-relative path does not carry one.
func UnresolvedIn(entries []string, reach map[string]bool) []string {
	out := []string{}
	for _, s := range entries {
		if i := strings.Index(s, ": "); i > 0 && reach[s[:i]] {
			out = append(out, s)
		}
	}
	return out
}

// Normalize sorts the edges by via, from and to, and drops the repeated ones. It never returns nil: an empty report
// is written the same way on every run.
func Normalize(edges []contract.DiscoveredEdge) []contract.DiscoveredEdge {
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Via != b.Via {
			return a.Via < b.Via
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	out := []contract.DiscoveredEdge{}
	for i, e := range edges {
		if i == 0 || e != edges[i-1] {
			out = append(out, e)
		}
	}
	return out
}

func unique(ss []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
