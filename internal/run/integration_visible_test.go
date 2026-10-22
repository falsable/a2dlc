package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A specifier's input folder holds the integration contracts of config.integration at their repo paths; a builder's holds
// none. The copy comes from the working tree, so a contract written after the last commit is what the specifier sees.
func TestSpecifierInputHoldsIntegrationContractsAndBuilderDoesNot(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],`+
		`"integration":{"consumers":[{"name":"web","image":"x","command":"run","contracts":["contracts/api/*.json"]}]}}`)
	write(t, dir, "contracts/api/orders.json", "{\"openapi\":\"3.0.0\"}\n")
	m := &Machine{app: a}

	specIn := t.TempDir()
	if err := m.copyIntegrationContracts(contract.RoleSpecifierAlpha, specIn); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(specIn, "contracts", "api", "orders.json")); err != nil || string(b) != "{\"openapi\":\"3.0.0\"}\n" {
		t.Fatalf("specifier /in lacks the contract: %v %q", err, b)
	}

	builderIn := t.TempDir()
	if err := m.copyIntegrationContracts(contract.RoleBuilderPrimary, builderIn); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(builderIn, "contracts")); !os.IsNotExist(err) {
		t.Fatalf("builder /in holds integration contracts: %v", err)
	}
}
