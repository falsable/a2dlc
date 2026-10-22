package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// ProposeRole and ProposeStage name the single call of `a2dlc propose`. It is not a protocol role: no stage of the state
// machine launches it and it sees no artifact of a run. It is in the table below so that roles.json can name it, and
// the strict decoding of roles.json accepts it.
const (
	ProposeRole  = "propose_properties"
	ProposeStage = "propose"
)

// Artifacts of a run, in .a2dlc/run/<slice>/. They are the only names that a role can see by engine in /in.
const (
	ArtIntent          = "intent.json"
	ArtCapture         = "capture.json"
	ArtContractAlpha   = "contract_alpha.json"
	ArtContractBeta    = "contract_beta.json"
	ArtContract        = "contract.json" // S*, the closed contractJSON
	ArtProbes          = "probes.json"   // probes locked with response signed
	ArtCatalogue       = "catalogue.json"
	ArtBacktranslation = "backtranslation.json"
	ArtBTReview        = "backtranslation_review.json"
	ArtRoute           = "route.json"
	ArtSignatures      = "signatures.json" // public signatures of the candidate (operation: signatures)
	ArtTier0           = "tier0_feedback.json"
	ArtEscalations     = "escalations.json"
	ArtVerdict         = "verdict.json"
	ArtCandidate       = "candidate.json" // the commit of the builder that the Stage 4 judges (block 3)
)

// visible is the table of §3 of the protocol ("Sees"), fixed in the binary: roles.json chooses the command, not the mounts.
// A role receives from these artifacts the ones that exist at the moment it is launched; never others.
//   - Specifiers: the intent and, for refactor, the capture. They do not see the code of other slices or the contracts of the other.
//   - Inquisitor: the two candidate contracts, the catalogue and the discrepancies of the back-translation. Neither builder code nor saboteur tests. In Stage 1.5 it compares the back-translation with the intent (§5 step 5): both reach it in the payload (inquisitor_bt_request), not by engine /in, because this table is by role and mounting intent.json for it would also show it to Stage 1.
//   - Back-translator: only S* (with π_S inside). Neither the intent nor the reasoning of the specifiers.
//   - Builders: S*, the locked probes and the Tier 0 feedback, plus /work. Neither the saboteur suite nor the /work of the other.
//     And the intent, whenever it exists: without S* (lite mode) it is the only thing that says what to build, and with S* (greenfield,
//     brownfield, refactor) they see it too. It is a deviation from the "Sees" list of §3, declared in the deviations table
//     of the README; the table is not conditional (block 3).
//     With N=2 each builder sees only its own Tier 0 feedback (block 8): the primary's names files, lines and
//     messages of its /work, and §3 forbids the orthogonal one the workspace of the other. Neither sees candidate.json, selection.json
//     nor the verdicts.
//   - Saboteur: S*, the public signatures and the escalations (lines without cover, mutants alive), plus the intent whenever
//     it exists (lite is the only one that says what the change was supposed to do; with S* it is the same deviation of §3 as for
//     the builders). Never the builder's code.
var visible = map[string][]string{
	contract.RoleSpecifierAlpha:    {ArtIntent, ArtCapture},
	contract.RoleSpecifierBeta:     {ArtIntent, ArtCapture},
	contract.RoleInquisitor:        {ArtContractAlpha, ArtContractBeta, ArtCatalogue, ArtBTReview},
	contract.RoleBackTranslator:    {ArtContract},
	contract.RoleBuilderPrimary:    {ArtContract, ArtProbes, ArtTier0, ArtIntent},
	contract.RoleBuilderOrthogonal: {ArtContract, ArtProbes, ArtTier0Orthogonal, ArtIntent},
	contract.RoleSaboteur:          {ArtContract, ArtSignatures, ArtEscalations, ArtIntent},
	ProposeRole:                    {}, // no run artifacts: propose builds its own /in from the diff
}

// Visible returns the artifacts that the policy lets a role see; nil for an unknown role.
func Visible(role string) []string { return visible[role] }

// WithWorkdir says whether the role receives /work (writable copy of the tree): only the builders.
func WithWorkdir(role string) bool {
	return role == contract.RoleBuilderPrimary || role == contract.RoleBuilderOrthogonal
}

// IntegrationGlobs are the globs of config.integration that name contract files a specifier may see: the provider's
// contracts that each consumer depends on (consumers[].contracts, the same globs that decide whether a consumer runs)
// and the provider's generated clients. The consumers' own contract files live in their repos, not in this one.
func IntegrationGlobs(ic *contract.IntegrationConfig) []string {
	if ic == nil {
		return nil
	}
	var globs []string
	for _, c := range ic.Consumers {
		globs = append(globs, c.Contracts...)
	}
	return append(globs, ic.GeneratedClients...)
}

// IntegrationVisible returns, among the paths of tree (repo-relative), the contract files that config.integration declares
// and that role may see. Only the specifiers do: they design against the upstream and downstream contracts of the graph
// (§3). Builders and the Saboteur see none of them, and so does any role that is not a specifier.
func IntegrationVisible(role string, ic *contract.IntegrationConfig, tree []string) []string {
	if role != contract.RoleSpecifierAlpha && role != contract.RoleSpecifierBeta {
		return nil
	}
	matched, _ := app.Partition(IntegrationGlobs(ic), tree)
	return matched
}

// ProposeRoleConfig returns the propose_properties entry of roles.json, checked the way readRoles checks every role,
// tier_up included: propose never runs it, but a roles.json that readRoles would refuse must not pass propose.
func ProposeRoleConfig(rs contract.Roles) (contract.Role, error) {
	r, ok := rs.Roles[ProposeRole]
	if !ok {
		return r, fmt.Errorf("%s has no %s role", pathRoles, ProposeRole)
	}
	if err := validateRoleConfig(ProposeRole, r); err != nil {
		return r, err
	}
	if r.TierUp != nil {
		if err := validateRoleConfig(ProposeRole+".tier_up", *r.TierUp); err != nil {
			return r, err
		}
		if r.TierUp.TierUp != nil {
			return r, fmt.Errorf("%s: %s.tier_up cannot have its own tier_up (CB-02b runs only one extra round)", pathRoles, ProposeRole)
		}
	}
	return r, nil
}

// WriteProposeRequest hashes the inputs already in inDir, writes request.json next to them (as Invoke does for a role) and
// returns the request with its inputs. runID is what the response must echo: run-<12 hex> as in run-defs.
func WriteProposeRequest(inDir, runID string, payload any) (contract.RoleRequest, error) {
	inputs, err := hashFilesUnder(inDir, "/in")
	if err != nil {
		return contract.RoleRequest{}, err
	}
	pl, err := json.Marshal(payload)
	if err != nil {
		return contract.RoleRequest{}, err
	}
	req := contract.RoleRequest{SchemaVersion: 1, RunID: runID, SliceID: "propose", Mode: contract.ModeLite, Role: ProposeRole,
		Stage: ProposeStage, Attempt: 1, Inputs: inputs, Payload: pl}
	rb, err := json.MarshalIndent(req, "", " ")
	if err != nil {
		return contract.RoleRequest{}, err
	}
	if err := os.WriteFile(filepath.Join(inDir, "request.json"), append(rb, '\n'), 0o644); err != nil {
		return contract.RoleRequest{}, err
	}
	req.Inputs = append([]contract.HashedFile{{Path: "/in/request.json", SHA256: hashBytes(append(rb, '\n'))}}, inputs...)
	return req, nil
}

// ValidateProposeResponse reads response.json of the propose call the way Invoke reads a role's: strict decoding, the
// identity of the request (run, role, stage, attempt 1), and the status and cost rules. It returns the reason when the
// response is not valid, "" otherwise.
func ValidateProposeResponse(b []byte, runID string) (contract.RoleResponse, string) {
	var resp contract.RoleResponse
	if err := contract.DecodeStrictBytes(b, &resp); err != nil {
		return resp, err.Error()
	}
	if resp.RunID != runID || resp.Role != ProposeRole || resp.Stage != ProposeStage || resp.Attempt != 1 {
		return resp, "the response does not answer this request"
	}
	if p := bytes.TrimSpace(resp.Payload); len(p) == 0 || p[0] != '{' {
		return resp, "payload is not an object"
	}
	if why := invalidResponse(resp); why != "" {
		return resp, why
	}
	if resp.Status == contract.RoleOK {
		if _, err := DecodeProposeProperties(resp); err != nil {
			return resp, "payload does not match propose_response: " + err.Error()
		}
	}
	return resp, ""
}

// DecodeProposeProperties reads the payload of an ok response with the strict decoding of propose_response
// (role-payloads.schema.json#/$defs/propose_response): a property names its target, its prose and its file.
func DecodeProposeProperties(resp contract.RoleResponse) ([]contract.ProposedProperty, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(resp.Payload, &keys); err != nil {
		return nil, err
	}
	if _, ok := keys["properties"]; !ok {
		return nil, fmt.Errorf("properties is required")
	}
	var pr contract.ProposeResponse
	if err := contract.DecodeStrictBytes(resp.Payload, &pr); err != nil {
		return nil, err
	}
	// what the schema constrains and the struct does not: target_id pattern and non-empty prose and file
	for i, p := range pr.Properties {
		switch {
		case !proposeTargetRe.MatchString(p.Target):
			return nil, fmt.Errorf("properties[%d].target %q is not <path>::<symbol>", i, p.Target)
		case strings.TrimSpace(p.Property) == "":
			return nil, fmt.Errorf("properties[%d].property is empty", i)
		case strings.TrimSpace(p.File) == "":
			return nil, fmt.Errorf("properties[%d].file is empty", i)
		}
	}
	return pr.Properties, nil
}

// proposeTargetRe is defs.schema.json#/$defs/target_id.
var proposeTargetRe = regexp.MustCompile(`^[^\\]+::.+$`)
