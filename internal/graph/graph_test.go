package graph

import (
	"fmt"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const openAPIYAMLDoc = `openapi: 3.0.3
info:
  title: orders
paths:
  /orders:
    get:
      summary: list
  "/orders/{id}/items":
    get:
      summary: items
  /v1:
    get:
      summary: too short
components:
  schemas:
    Order: {}
`

func TestContractOfOpenAPIYAML(t *testing.T) {
	c, ok := ContractOf("api/openapi.yaml", []byte(openAPIYAMLDoc))
	if !ok || c.Via != ViaOpenAPI {
		t.Fatalf("%+v %v", c, ok)
	}
	// "orders" from both paths, "items" is static, "{id}" is a template and "v1" is too short
	if fmt.Sprint(c.Names) != "[items orders]" {
		t.Fatalf("names %v", c.Names)
	}
}

func TestContractOfOpenAPIJSONAndSwagger(t *testing.T) {
	c, ok := ContractOf("api/spec.json", []byte(`{"openapi":"3.1.0","paths":{"/invoices":{},"/refunds/{id}":{}}}`))
	if !ok || c.Via != ViaOpenAPI || fmt.Sprint(c.Names) != "[invoices refunds]" {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := ContractOf("api/old.yml", []byte("swagger: \"2.0\"\npaths:\n  /tickets:\n    get: {}\n")); !ok {
		t.Fatal("swagger 2.0 YAML not read as a contract")
	}
}

// A YAML or JSON file that is not an OpenAPI document, and a document whose paths are not in the paths block, are not
// contracts: they would otherwise link every config file of the repo to the clients.
func TestContractOfIsNotAnyDocument(t *testing.T) {
	for file, content := range map[string]string{
		"ci/pipeline.yaml": "name: ci\njobs:\n  build:\n    steps: []\n",
		"package.json":     `{"name":"web","paths":{"/x":{}}}`,
		"api/notes.yaml":   "# openapi: documented elsewhere\nnotes:\n  /orders:\n    x: 1\n",
		"api/broken.json":  `{"openapi":`,
		"src/readme.md":    "openapi: 3.0\n",
	} {
		if c, ok := ContractOf(file, []byte(content)); ok {
			t.Errorf("%s read as contract %+v", file, c)
		}
	}
}

func TestContractOfProto(t *testing.T) {
	src := "syntax = \"proto3\";\npackage shop;\nservice OrderService {\n  rpc Get (GetRequest) returns (Order);\n}\nmessage Order {\n  string id = 1;\n}\nmessage GetRequest {}\nmessage Id {}\n"
	c, ok := ContractOf("proto/shop.proto", []byte(src))
	if !ok || c.Via != ViaProtobuf {
		t.Fatalf("%+v %v", c, ok)
	}
	// "Id" is shorter than minName and is not a name
	if fmt.Sprint(c.Names) != "[GetRequest Order OrderService]" {
		t.Fatalf("names %v", c.Names)
	}
	if _, ok := ContractOf("proto/empty.proto", []byte("syntax = \"proto3\";\n")); ok {
		t.Fatal("a proto without service or message is a contract")
	}
}

// A generated client is linked when one of the contract's names is one of its words: "orders" links orders.ts but
// not ordersArchive.ts, which only contains the longer identifier.
func TestContractEdgesLinkByWholeWordFromTheContract(t *testing.T) {
	contracts := map[string]Contract{
		"api/openapi.yaml": {Via: ViaOpenAPI, Names: []string{"orders"}},
		"proto/shop.proto": {Via: ViaProtobuf, Names: []string{"Order"}},
	}
	clients := map[string][]byte{
		"clients/gen/orders.ts":        []byte("export const path = '/orders/' + id;\n"),
		"clients/gen/ordersArchive.ts": []byte("export const ordersArchive = 1;\n"),
		"clients/gen/shop.ts":          []byte("export interface Order { id: string }\n"),
	}
	edges := ContractEdges(contracts, clients)
	want := "[clients/gen/orders.ts]"
	got := fmt.Sprint(edgeTargets(edges, ViaOpenAPI))
	if got != want || fmt.Sprint(edgeTargets(edges, ViaProtobuf)) != "[clients/gen/shop.ts]" || len(edges) != 2 {
		t.Fatalf("%v", edges)
	}
	if edges[0] != (contract.DiscoveredEdge{From: "api/openapi.yaml", To: "clients/gen/orders.ts", Via: ViaOpenAPI}) {
		t.Fatalf("%+v", edges[0])
	}
}

func TestContractEdgesOnlyForTheContractsGiven(t *testing.T) {
	clients := map[string][]byte{"gen/a.ts": []byte("orders")}
	if edges := ContractEdges(map[string]Contract{}, clients); len(edges) != 0 || edges == nil {
		t.Fatalf("%v", edges)
	}
	// a contract that is itself a generated client is not linked to itself
	edges := ContractEdges(map[string]Contract{"gen/a.ts": {Via: ViaOpenAPI, Names: []string{"orders"}}}, clients)
	if len(edges) != 0 {
		t.Fatalf("%v", edges)
	}
}

func TestImportEdgesReachTheChangedModuleOnly(t *testing.T) {
	deps := []contract.DepsEdge{
		{From: "src/app.py", To: "src/calc.py"},
		{From: "src/calc.py", To: "src/util.py"},
		{From: "src/app.py", To: "src/app.py"},
		{From: "tests/test_calc.py", To: "src/calc.py"},
	}
	got := ImportEdges(deps, map[string]bool{"src/calc.py": true})
	want := []contract.DiscoveredEdge{
		{From: "src/app.py", To: "src/calc.py", Via: ViaImport},
		{From: "tests/test_calc.py", To: "src/calc.py", Via: ViaImport},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v", got)
	}
	if ImportEdges(nil, nil) == nil || len(ImportEdges(deps, map[string]bool{"x.py": true})) != 0 {
		t.Fatal("no changed file gives no edges (and never nil)")
	}
}

// The consumers of a changed file are the inverse of its import edges, transitively: an importer of an importer is in
// the blast radius too. The consumer lookup reads the edges as the engine gives them and never fails on the unresolved.
func TestReachInvertsTheImportEdgesTransitively(t *testing.T) {
	deps := []contract.DepsEdge{
		{From: "src/app.py", To: "src/calc.py"},
		{From: "src/main.py", To: "src/app.py"},
		{From: "src/calc.py", To: "src/util.py"},
		{From: "src/other.py", To: "src/util.py"},
		{From: "src/x.py", To: ""},
	}
	got := Reach(deps, map[string]bool{"src/calc.py": true})
	if fmt.Sprint(got) != "map[src/app.py:true src/calc.py:true src/main.py:true]" {
		t.Fatalf("%v", got)
	}
	if len(Reach(nil, nil)) != 0 {
		t.Fatal("nothing changed reaches nothing")
	}
}

func TestUnresolvedInKeepsTheImportsOfReachingFilesAsReported(t *testing.T) {
	reach := map[string]bool{"src/app.py": true, "src/calc.py": true}
	in := []string{"src/app.py: pandas", "src/other.py: numpy", "src/calc.py: ./missing", "pandas"}
	got := UnresolvedIn(in, reach)
	if fmt.Sprint(got) != "[src/app.py: pandas src/calc.py: ./missing]" {
		t.Fatalf("%v", got)
	}
	if UnresolvedIn(nil, reach) == nil {
		t.Fatal("never nil")
	}
}

func TestNormalizeSortsAndDropsRepeats(t *testing.T) {
	in := []contract.DiscoveredEdge{
		{From: "b", To: "x", Via: ViaImport},
		{From: "a", To: "y", Via: ViaProtobuf},
		{From: "b", To: "x", Via: ViaImport},
		{From: "a", To: "x", Via: ViaImport},
	}
	got := fmt.Sprint(Normalize(in))
	if got != "[{a x import} {b x import} {a y protobuf}]" {
		t.Fatalf("%s", got)
	}
}

func edgeTargets(edges []contract.DiscoveredEdge, via string) []string {
	var out []string
	for _, e := range edges {
		if e.Via == via {
			out = append(out, e.To)
		}
	}
	return out
}
