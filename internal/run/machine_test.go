package run

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// §5's family rule: the specifiers and the N=2 builders of different families; family_exception with a reason allows a pair.
func TestCheckFamilies(t *testing.T) {
	role := func(family string) contract.Role {
		return contract.Role{Family: family, Image: "i:1", Argv: []string{"x"}}
	}
	exception := &contract.FamilyException{Reason: "only one provider is licensed for this slice"}
	cases := []struct {
		name  string
		roles map[string]contract.Role
		fe    *contract.FamilyException
		err   string // substring of the error; "" means accepted
	}{
		{"specifiers differ", map[string]contract.Role{contract.RoleSpecifierAlpha: role("claude"), contract.RoleSpecifierBeta: role("gemini")}, nil, ""},
		{"specifiers equal", map[string]contract.Role{contract.RoleSpecifierAlpha: role("claude"), contract.RoleSpecifierBeta: role("claude")}, nil, "specifier_alpha and specifier_beta"},
		{"builders equal", map[string]contract.Role{contract.RoleBuilderPrimary: role("gemini"), contract.RoleBuilderOrthogonal: role("gemini")}, nil, "builder_primary and builder_orthogonal"},
		{"equal with exception", map[string]contract.Role{contract.RoleSpecifierAlpha: role("claude"), contract.RoleSpecifierBeta: role("claude")}, exception, ""},
		{"one specifier is not configured", map[string]contract.Role{contract.RoleSpecifierAlpha: role("claude")}, nil, ""},
		{"empty reason", map[string]contract.Role{contract.RoleSpecifierAlpha: role("claude")}, &contract.FamilyException{}, "family_exception without a reason"},
	}
	for _, c := range cases {
		err := checkFamilies(contract.Roles{SchemaVersion: 1, Roles: c.roles, FamilyException: c.fe})
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: error %v, want one naming %q", c.name, err, c.err)
		}
	}
}

// The rule runs when the roles load: a run with two equal-family specifiers stops before any stage, and with the exception it
// goes on, and the note says why.
func TestSameFamilyRolesAreRefusedAtLoad(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"specifier_alpha":{"family":"claude","image":"a:1","argv":["x"]},`+
		`"specifier_beta":{"family":"claude","image":"b:1","argv":["x"]}}}`)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"}); err == nil || !strings.Contains(err.Error(), "family") {
		t.Fatalf("%v", err)
	}
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"family_exception":{"reason":"one licence in this slice"},"roles":{"specifier_alpha":{"family":"claude","image":"a:1","argv":["x"]},`+
		`"specifier_beta":{"family":"claude","image":"b:1","argv":["x"]}}}`)
	m := &Machine{roles: contract.Roles{FamilyException: &contract.FamilyException{Reason: "one licence in this slice"}}}
	if got := m.familyExceptionNote(); got != "family_exception: one licence in this slice" {
		t.Fatalf("note %q", got)
	}
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"}); err != nil {
		t.Fatalf("with family_exception the run loads: %v", err)
	}
}

// CB-02b gives one extra round: Role.UnmarshalJSON refuses a tier_up with its own tier_up, so the roles file does not load.
func TestNestedTierUpIsRefusedWhenRolesLoad(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"a:1","argv":["x"],`+
		`"tier_up":{"family":"claude","image":"b:1","argv":["x"],"tier_up":{"family":"claude","image":"c:1","argv":["x"]}}}}}`)
	m := &Machine{app: a}
	if err := m.readRoles(); err == nil || !strings.Contains(err.Error(), "own tier_up") {
		t.Fatalf("nested tier_up loaded: %v", err)
	}
}
