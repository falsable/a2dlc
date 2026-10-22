package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestIntegrationVisibleGivesOnlySpecifiersTheDeclaredContracts(t *testing.T) {
	ic := &contract.IntegrationConfig{
		GeneratedClients: []string{"gen/**"},
		Consumers: []contract.ConsumerConfig{
			{Name: "web", Image: "i", Command: "c", Contracts: []string{"api/openapi.yaml"}},
			{Name: "crm", Image: "i", Command: "c", Contracts: []string{"proto/*.proto"}},
		},
	}
	tree := []string{"api/openapi.yaml", "proto/order.proto", "gen/client.go", "src/app.go"}
	for _, role := range []string{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta} {
		got := IntegrationVisible(role, ic, tree)
		if strings.Join(got, ",") != "api/openapi.yaml,proto/order.proto,gen/client.go" {
			t.Fatalf("%s sees %v", role, got)
		}
	}
	for _, role := range []string{contract.RoleBuilderPrimary, contract.RoleBuilderOrthogonal, contract.RoleSaboteur,
		contract.RoleInquisitor, contract.RoleBackTranslator, ProposeRole} {
		if got := IntegrationVisible(role, ic, tree); got != nil {
			t.Fatalf("%s must not see integration contracts, sees %v", role, got)
		}
	}
}

func TestIntegrationVisibleWithoutConfigOrGlobsSeesNothing(t *testing.T) {
	tree := []string{"api/openapi.yaml"}
	if got := IntegrationVisible(contract.RoleSpecifierAlpha, nil, tree); got != nil {
		t.Fatalf("nil config: %v", got)
	}
	ic := &contract.IntegrationConfig{Consumers: []contract.ConsumerConfig{{Name: "web", Image: "i", Command: "c"}}}
	if got := IntegrationVisible(contract.RoleSpecifierBeta, ic, tree); len(got) != 0 {
		t.Fatalf("consumer without contracts: %v", got)
	}
}

func TestIntegrationGlobsAreTheContractsAndGeneratedClients(t *testing.T) {
	ic := &contract.IntegrationConfig{GeneratedClients: []string{"gen/**"},
		Consumers: []contract.ConsumerConfig{{Contracts: []string{"a.yaml"}}, {Contracts: []string{"b.proto", "c.proto"}}}}
	if got := strings.Join(IntegrationGlobs(ic), ","); got != "a.yaml,b.proto,c.proto,gen/**" {
		t.Fatalf("globs = %s", got)
	}
	if IntegrationGlobs(nil) != nil {
		t.Fatal("nil config must give no globs")
	}
}

func TestVisibleAcceptsTheProposeRoleAndNoOtherUnknownRole(t *testing.T) {
	if Visible(ProposeRole) == nil {
		t.Fatal("roles.json must be able to name propose_properties")
	}
	if Visible("oracle") != nil {
		t.Fatal("unknown roles must stay unknown")
	}
	if len(Visible(ProposeRole)) != 0 {
		t.Fatalf("propose sees run artifacts: %v", Visible(ProposeRole))
	}
}

func TestProposeRoleConfigNeedsTheRoleArgvAndImage(t *testing.T) {
	ok := contract.Role{Family: "claude", Image: "a2dlc-roles:0.1", Argv: []string{"python3", "/adapter.py"}}
	if _, err := ProposeRoleConfig(contract.Roles{Roles: map[string]contract.Role{ProposeRole: ok}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProposeRoleConfig(contract.Roles{Roles: map[string]contract.Role{}}); err == nil || !strings.Contains(err.Error(), "has no propose_properties role") {
		t.Fatalf("missing role: %v", err)
	}
	noArgv := ok
	noArgv.Argv = nil
	if _, err := ProposeRoleConfig(contract.Roles{Roles: map[string]contract.Role{ProposeRole: noArgv}}); err == nil {
		t.Fatal("a role without argv must be refused")
	}
	badImage := ok
	badImage.Image = "--volume=/:/host"
	if _, err := ProposeRoleConfig(contract.Roles{Roles: map[string]contract.Role{ProposeRole: badImage}}); err == nil {
		t.Fatal("an image that starts with a flag must be refused")
	}
}

func TestWriteProposeRequestHashesTheInputsAndWritesRequestJSON(t *testing.T) {
	in := t.TempDir()
	if err := os.MkdirAll(filepath.Join(in, "candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "candidate", "f.py"), []byte("def f(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runID := "run-0123456789ab"
	req, err := WriteProposeRequest(in, runID, map[string]any{"changed": []string{"f.py"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.RunID != runID || req.Role != ProposeRole || req.Stage != ProposeStage || req.Attempt != 1 || req.Mode != contract.ModeLite {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Inputs) != 2 || req.Inputs[0].Path != "/in/request.json" || req.Inputs[1].Path != "/in/candidate/f.py" {
		t.Fatalf("inputs = %+v", req.Inputs)
	}
	raw, err := os.ReadFile(filepath.Join(in, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk contract.RoleRequest
	if err := contract.DecodeStrictBytes(raw, &onDisk); err != nil {
		t.Fatalf("request.json does not decode strictly: %v", err)
	}
	if len(onDisk.Inputs) != 1 || onDisk.Inputs[0].Path != "/in/candidate/f.py" {
		t.Fatalf("request.json inputs = %+v (the file lists the inputs without itself)", onDisk.Inputs)
	}
}

func proposeResponse(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{"schema_version": 1, "run_id": "run-0123456789ab", "role": ProposeRole, "stage": ProposeStage,
		"attempt": 1, "status": "ok", "cost": nil, "payload": map[string]any{"properties": []any{}}}
	mutate(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// anyProposeMismatch stands for "some propose_response mismatch" in the table below, for cases whose decoder message is
// the one of the strict decoding.
const anyProposeMismatch = "<any propose_response mismatch>"

// roles.json may name propose_properties: the strict decoding of the roles contract accepts it, and so do the policy
// table and the propose check of the role config.
func TestRolesJSONMayNamePropose(t *testing.T) {
	const raw = `{"schema_version":1,"roles":{"propose_properties":{"family":"claude","image":"a2dlc-roles:0.1","argv":["python3","/adapter.py"]}}}`
	var rs contract.Roles
	if err := contract.DecodeStrictBytes([]byte(raw), &rs); err != nil {
		t.Fatalf("roles.json naming propose_properties does not decode: %v", err)
	}
	if _, err := ProposeRoleConfig(rs); err != nil {
		t.Fatal(err)
	}
}

func TestProposeRoleConfigChecksTierUpLikeReadRoles(t *testing.T) {
	ok := contract.Role{Family: "claude", Image: "a2dlc-roles:0.1", Argv: []string{"python3", "/adapter.py"}}
	withTier := func(tier contract.Role) contract.Roles {
		r := ok
		r.TierUp = &tier
		return contract.Roles{Roles: map[string]contract.Role{ProposeRole: r}}
	}
	if _, err := ProposeRoleConfig(withTier(ok)); err != nil {
		t.Fatalf("a valid tier_up is accepted: %v", err)
	}
	badImage := ok
	badImage.Image = "--volume=/:/host"
	if _, err := ProposeRoleConfig(withTier(badImage)); err == nil || !strings.Contains(err.Error(), "propose_properties.tier_up") {
		t.Fatalf("a tier_up with a bad image must be refused, got %v", err)
	}
	noArgv := ok
	noArgv.Argv = nil
	if _, err := ProposeRoleConfig(withTier(noArgv)); err == nil {
		t.Fatal("a tier_up without argv must be refused")
	}
	nested := ok
	nested.TierUp = &ok
	if _, err := ProposeRoleConfig(withTier(nested)); err == nil || !strings.Contains(err.Error(), "cannot have its own tier_up") {
		t.Fatalf("a tier_up inside a tier_up must be refused, got %v", err)
	}
}

func TestValidateProposeResponseChecksIdentityStatusAndPayload(t *testing.T) {
	const runID = "run-0123456789ab"
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		reason string
	}{
		{"valid", func(m map[string]any) {}, ""},
		{"another run", func(m map[string]any) { m["run_id"] = "run-ffffffffffff" }, "the response does not answer this request"},
		{"another role", func(m map[string]any) { m["role"] = "saboteur" }, "the response does not answer this request"},
		{"another stage", func(m map[string]any) { m["stage"] = "1" }, "the response does not answer this request"},
		{"another attempt", func(m map[string]any) { m["attempt"] = 2 }, "the response does not answer this request"},
		{"unknown status", func(m map[string]any) { m["status"] = "maybe" }, `status "maybe" is not ok|refused|error`},
		{"refused without reason", func(m map[string]any) { m["status"] = "refused" }, "status refused without reason"},
		{"negative cost", func(m map[string]any) { m["cost"] = map[string]any{"usd": -1} }, "negative cost"},
		{"payload is a list", func(m map[string]any) { m["payload"] = []any{} }, "payload is not an object"},
		{"payload is null", func(m map[string]any) { m["payload"] = nil }, "payload is not an object"},
		{"ok without properties", func(m map[string]any) { m["payload"] = map[string]any{"summary": "x"} },
			"payload does not match propose_response: properties is required"},
		{"ok with a property of another shape", func(m map[string]any) {
			m["payload"] = map[string]any{"properties": []any{map[string]any{"target": "a::b", "property": 1}}}
		}, anyProposeMismatch},
		{"ok with an empty property", func(m map[string]any) {
			m["payload"] = map[string]any{"properties": []any{map[string]any{"target": "a.py::f", "property": " ", "file": "tests/test_a.py"}}}
		}, "payload does not match propose_response: properties[0].property is empty"},
		{"ok with a target outside the pattern", func(m map[string]any) {
			m["payload"] = map[string]any{"properties": []any{map[string]any{"target": "a.py f", "property": "f is total", "file": "tests/test_a.py"}}}
		}, `payload does not match propose_response: properties[0].target "a.py f" is not <path>::<symbol>`},
		{"ok with an empty file", func(m map[string]any) {
			m["payload"] = map[string]any{"properties": []any{map[string]any{"target": "a.py::f", "property": "f is total", "file": ""}}}
		}, "payload does not match propose_response: properties[0].file is empty"},
		{"ok with an unknown payload field", func(m map[string]any) {
			m["payload"] = map[string]any{"properties": []any{}, "extra": 1}
		}, anyProposeMismatch},
		{"refused payload is not checked against propose_response", func(m map[string]any) {
			m["status"], m["reason"], m["payload"] = "refused", "no public function", map[string]any{"summary": "x"}
		}, ""},
		{"unknown field", func(m map[string]any) { m["extra"] = true }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, why := ValidateProposeResponse(proposeResponse(t, c.mutate), runID)
			if c.reason == anyProposeMismatch {
				if !strings.HasPrefix(why, "payload does not match propose_response: ") {
					t.Fatalf("reason = %q, want a propose_response mismatch", why)
				}
				return
			}
			if c.name == "unknown field" {
				if why == "" {
					t.Fatal("a field the contract does not have must not decode")
				}
				return
			}
			if why != c.reason {
				t.Fatalf("reason = %q, want %q", why, c.reason)
			}
		})
	}
}
