package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// Invocation is the result of a role after its retries: the valid response (nil if none was valid), the /out folder of
// the attempt that counted, and the ledger entries it left.
type Invocation struct {
	Response *contract.RoleResponse
	OutDir   string
	Entries  []contract.LedgerEntry
}

// Invoke launches a role: builds /in with request.json and the artifacts that the policy lets it see, runs it, validates
// the response against contractJSON and records each attempt in the ledger. It retries up to roles.<role>.retries when
// the command fails, times out or the response does not validate; a refused is not retried (it is not a tool error).
func (m *Machine) Invoke(ctx context.Context, role, stage string, payload any, workdir string) (Invocation, error) {
	r, ok := m.roles.Roles[role]
	if !ok {
		return Invocation{}, fmt.Errorf("role %s is not configured in %s", role, pathRoles)
	}
	return m.invokeWith(ctx, role, r, stage, payload, workdir)
}

// copyIntegrationContracts puts the contracts that config.integration declares into the input folder of a specifier, each
// at its repo path (IntegrationVisible decides which; every other role gets none). The files are read from the working
// tree's listing, as the Stage 1 capture reads them, so the specifier designs against the graph's current contracts.
func (m *Machine) copyIntegrationContracts(role, in string) error {
	if role != contract.RoleSpecifierAlpha && role != contract.RoleSpecifierBeta {
		return nil
	}
	cfg, err := m.app.Config()
	if err != nil {
		return err
	}
	if cfg.Integration == nil {
		return nil
	}
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return err
	}
	files, err := m.app.Repo.Files(tree)
	if err != nil {
		return err
	}
	for _, f := range IntegrationVisible(role, cfg.Integration, files) {
		b, err := m.app.Repo.Show(tree, f)
		if err != nil {
			return fmt.Errorf("integration contract %s: %w", f, err)
		}
		dst := filepath.Join(in, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// invokeWith launches the role with configuration r: the one from roles.json, or the tier_up one when Stage 3 exhausts
// the builder (CB-02b). The role's name does not change: the ledger, /in and the artifact policy stay the builder's.
func (m *Machine) invokeWith(ctx context.Context, role string, r contract.Role, stage string, payload any, workdir string) (Invocation, error) {
	var invocation Invocation
	if WithWorkdir(role) != (workdir != "") {
		return invocation, fmt.Errorf("role %s: workdir %q contradicts the policy", role, workdir)
	}
	retries := 1
	if r.Retries != nil {
		retries = *r.Retries
	}
	pl, err := json.Marshal(payload)
	if err != nil {
		return invocation, err
	}
	for intent := 1; intent <= retries+1; intent++ {
		if m.exceeded() != "" {
			return invocation, errCB03 // CB-03 before each launchRecord: the limit cuts in the middle of a loop, not at the end of the stage
		}
		dir := filepath.Join(m.dir, "launches", fmt.Sprintf("%03d-%s-%s-%d", len(m.ledger.doc.Entries)+1, stage, role, intent))
		in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
		if err := os.MkdirAll(in, 0o755); err != nil {
			return invocation, err
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return invocation, err
		}
		// only what the policy lets it see, and only if it exists
		for _, artifact := range Visible(role) {
			b, err := os.ReadFile(filepath.Join(m.dir, artifact))
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(in, artifact), b, 0o644); err != nil {
				return invocation, err
			}
		}
		if err := m.copyIntegrationContracts(role, in); err != nil {
			return invocation, err
		}
		inputs, err := hashFilesUnder(in, "/in")
		if err != nil {
			return invocation, err
		}
		var wd *string
		if workdir != "" {
			w := "/work"
			wd = &w
		}
		req := contract.RoleRequest{SchemaVersion: 1, RunID: m.state.RunID, SliceID: m.state.SliceID, Mode: m.state.Mode,
			Role: role, Stage: stage, Attempt: intent, Inputs: inputs, Workdir: wd, Payload: pl}
		rb, _ := json.MarshalIndent(req, "", " ")
		// request.json is written after hashing the rest, and is added to the list with its own hash
		if err := os.WriteFile(filepath.Join(in, "request.json"), append(rb, '\n'), 0o644); err != nil {
			return invocation, err
		}
		req.Inputs = append([]contract.HashedFile{{Path: "/in/request.json", SHA256: hashBytes(append(rb, '\n'))}}, inputs...)
		res, err := m.launcher.Launch(ctx, r, LaunchSpec{Role: role, Stage: stage, Attempt: intent, InDir: in, OutDir: out, WorkDir: workdir,
			Name: fmt.Sprintf("a2dlc-%s-%03d-%d", m.state.RunID, len(m.ledger.doc.Entries)+1, intent)})
		if err != nil {
			return invocation, fmt.Errorf("launch %s: %w", role, err)
		}
		outputs, err := hashFilesUnder(out, "/out")
		if err != nil {
			return invocation, fmt.Errorf("hash /out of %s: %w", role, err)
		}
		entry := contract.LedgerEntry{At: m.now().UTC().Format(timeRFC), Stage: stage, Role: role, Attempt: intent,
			Family: r.Family, Image: r.Image, ArgvSHA256: hashBytes([]byte(fmt.Sprintf("%q", res.Argv))),
			Inputs: req.Inputs, Outputs: outputs, Network: res.Network, EnvKeys: res.EnvKeys, Exit: res.Exit}
		if res.ImageDigest != "" {
			d := res.ImageDigest
			entry.ImageDigest = &d
		}
		entry.EgressProxy = res.EgressProxy
		var resp contract.RoleResponse
		switch {
		case res.TimedOut:
			entry.Status = contract.LedgerTimeout
		default:
			b, err := os.ReadFile(filepath.Join(out, "response.json"))
			if err != nil {
				entry.Status = contract.LedgerError
			} else if err := contract.DecodeStrictBytes(b, &resp); err != nil || resp.RunID != m.state.RunID || resp.Role != role || resp.Stage != stage || resp.Attempt != intent || invalidResponse(resp) != "" {
				entry.Status = contract.LedgerInvalidResponse
			} else {
				entry.Status, entry.Cost = resp.Status, resp.Cost
			}
		}
		e, err := m.ledger.Append(entry)
		if err != nil {
			return invocation, err
		}
		invocation.Entries = append(invocation.Entries, e)
		m.account(entry)
		if entry.Status == contract.RoleOK || entry.Status == contract.RoleRefused {
			invocation.Response, invocation.OutDir = &resp, out
			return invocation, nil
		}
	}
	return invocation, nil
}

// invalidResponse applies to response.json what role-response.schema.json requires and DecodeStrictBytes does not check
// (status enum, reason with refused/error, non-negative cost): an untrusted role writes it, and the ledger's status and
// CB-03's spend come from it. Empty if the response is valid.
func invalidResponse(r contract.RoleResponse) string {
	switch r.Status {
	case contract.RoleOK:
	case contract.RoleRefused, contract.RoleError:
		if r.Reason == "" {
			return "status " + r.Status + " without reason"
		}
	default:
		return fmt.Sprintf("status %q is not ok|refused|error", r.Status)
	}
	if c := r.Cost; c != nil {
		if why := c.Invalid(); why != "" {
			return why
		}
	}
	return ""
}

// NetworkMode is the ledger label of the network a role gets: none unless roles.json declares provider (the schema
// default, so an empty network is none). A provider role never gets a bridge without the proxy: the launcher gives it
// the internal network of the egress proxy, so its label is proxy-allowlist (see launcher.go, runWithEgress).
func NetworkMode(r contract.Role) string {
	if r.Network == "provider" {
		return NetworkProxyAllowlist
	}
	return NetworkNone
}

// networkOf is the ledger's label for the launch's network. It is the same value the launcher reports in
// LaunchResult.Network for the same role, which the launcher tests pin.
func networkOf(r contract.Role) string {
	return NetworkMode(r)
}

// account adds the reported cost to the budget; without a report, it counts the role as unreported (CB-03 says so).
func (m *Machine) account(e contract.LedgerEntry) {
	sumSpend(&m.state.Budget, e)
}

// sumSpend is the only place that writes SpentUSD, SpentTokens, ByRole and UnreportedRoles: account uses it at launch and
// spendOfLedger when rebuilding, and both sums give the same result because they add in the same order. The tokens are
// input plus output as the role's CLI reported them; a role that reported none adds nothing to SpentTokens. The input
// already includes the cache, so the cache counters are summed apart and never added to SpentTokens.
func sumSpend(b *contract.Budget, e contract.LedgerEntry) {
	if e.Cost != nil {
		b.SpentUSD += e.Cost.USD
		addTokens(&b.SpentTokens, e.Cost.InputTokens+e.Cost.OutputTokens)
		addTokens(&b.SpentCacheReadTokens, e.Cost.CacheReadInputTokens)
		addTokens(&b.SpentCacheCreationTokens, e.Cost.CacheCreationInputTokens)
		if b.ByRole == nil {
			b.ByRole = map[string]float64{}
		}
		b.ByRole[e.Role] += e.Cost.USD
	} else {
		b.UnreportedRoles++
	}
}

// addTokens adds n to a counter that stays nil until some role reports tokens for it.
func addTokens(sum **int, n int) {
	if n <= 0 {
		return
	}
	if *sum == nil {
		zero := 0
		*sum = &zero
	}
	**sum += n
}

// tokensOf is the budget's spent tokens, 0 when none was reported.
func tokensOf(b contract.Budget) int {
	if b.SpentTokens == nil {
		return 0
	}
	return *b.SpentTokens
}

// spendOfLedger recalculates the spend from the entries. The ledger is written on every launchRecord and state.json
// only when a stage starts and ends, so after an interruption the ledger knows more than the state.
func spendOfLedger(entries []contract.LedgerEntry) contract.Budget {
	var b contract.Budget
	for _, e := range entries {
		sumSpend(&b, e)
	}
	return b
}

// ledgerWithLessSpend says, if anything, what the ledger records less of than state.json: a deleted or truncated ledger
// cannot reset the budget. The ledger recording more is not a problem (interrupted stage).
func ledgerWithLessSpend(state, ledger contract.Budget) string {
	const eps = 1e-9
	switch {
	case ledger.SpentUSD < state.SpentUSD-eps:
		return fmt.Sprintf("the ledger sums %.4f USD and state.json %.4f", ledger.SpentUSD, state.SpentUSD)
	case tokensOf(ledger) < tokensOf(state):
		return fmt.Sprintf("the ledger sums %d tokens and state.json %d", tokensOf(ledger), tokensOf(state))
	case ledger.UnreportedRoles < state.UnreportedRoles:
		return fmt.Sprintf("the ledger counts %d roles without a report and state.json %d", ledger.UnreportedRoles, state.UnreportedRoles)
	}
	for role, usd := range state.ByRole {
		if ledger.ByRole[role] < usd-eps {
			return fmt.Sprintf("the ledger sums %.4f USD of %s and state.json %.4f", ledger.ByRole[role], role, usd)
		}
	}
	return ""
}
