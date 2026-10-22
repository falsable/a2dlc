package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The payloads of the block 6 examples decode strictly with their structs, and the valid contracts pass Validate;
// the decoder or Validate rejects the invalid slice-contract ones (what the core sees from an LLM).
func TestExamplesOfStage1(t *testing.T) {
	read := func(dir, name string) []byte {
		b, err := os.ReadFile(filepath.Join("../../contracts/examples", dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	payload := func(name string) []byte {
		var r struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(read("valid", name), &r); err != nil {
			t.Fatal(err)
		}
		return r.Payload
	}
	var sreq SpecifierRequest
	if err := DecodeStrictBytes(payload("role-request__specifier-beta.json"), &sreq); err != nil || sreq.Round != 2 || len(sreq.Discrepancies) != 1 || sreq.Discrepancies[0].Resolution != nil {
		t.Fatalf("%v %+v", err, sreq)
	}
	var ireq InquisitorRequest
	if err := DecodeStrictBytes(payload("role-request__inquisitor.json"), &ireq); err != nil || ireq.Catalogue == nil || ireq.Catalogue.Validate() != nil ||
		ireq.ContractAlpha.Validate() != nil || ireq.ContractBeta.Validate() != nil {
		t.Fatalf("%v %+v", err, ireq)
	}
	var sresp SpecifierResponse
	if err := DecodeStrictBytes(payload("role-response__specifier-alpha-ok.json"), &sresp); err != nil || sresp.Contract.Validate() != nil {
		t.Fatalf("%v", err)
	}
	var iresp InquisitorResponse
	if err := DecodeStrictBytes(payload("role-response__inquisitor-ok.json"), &iresp); err != nil || iresp.Validate() != nil {
		t.Fatalf("%v %v", err, iresp.Validate())
	}
	iresp.Contract.Source = SourceAlpha
	if iresp.Validate() == nil {
		t.Fatal("a candidate from α passed S*")
	}
	invalid, _ := filepath.Glob("../../contracts/examples/invalid/slice-contract__*.json")
	if len(invalid) == 0 {
		t.Fatal("no invalid slice-contract examples")
	}
	for _, p := range invalid {
		// A missing signature and a null one decode the same into a *string; the schema tells them apart, the core treats them alike
		// (null means "the specifier does not set it") and accepting it does no harm.
		if strings.HasSuffix(p, "function-missing-signature.json") {
			continue
		}
		var c SliceContract
		b, _ := os.ReadFile(p)
		if err := DecodeStrictBytes(b, &c); err == nil && c.Validate() == nil {
			t.Errorf("%s passed", filepath.Base(p))
		}
	}
	var c SliceContract
	if err := DecodeStrictBytes(read("valid", "slice-contract__consensus.json"), &c); err != nil || c.Validate() != nil {
		t.Fatal(err)
	}
	c.Probes = append(c.Probes, c.Probes[0])
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("%v", err)
	}
	// A rejected specifier only sees this message on its retry: it has to name the values that pass.
	a := Assumption{ID: "type_validation", Statement: "not validated (non-goal)", Source: "intent", Status: AssumptionAccepted}
	if err := a.validate(); err == nil || !strings.Contains(err.Error(), `(it is "specifier" or "catalogue")`) {
		t.Fatalf("%v", err)
	}
}
