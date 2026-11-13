package run

import (
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// SpendRow is what one (stage, role) pair of a run spent: its launches, the cost they reported and how many reported none.
type SpendRow struct {
	Stage, Role       string
	Calls, Unreported int
	Cost              contract.Cost
}

// SpendRows sums the ledger's launches per (stage, role), in the order each pair first launched. A binding entry launched
// nothing and is left out, as in spendOfLedger.
func SpendRows(entries []contract.LedgerEntry) []SpendRow {
	var rows []SpendRow
	at := map[[2]string]int{}
	for _, e := range entries {
		if e.Authorization != nil {
			continue
		}
		k := [2]string{e.Stage, e.Role}
		i, ok := at[k]
		if !ok {
			i, at[k] = len(rows), len(rows)
			rows = append(rows, SpendRow{Stage: e.Stage, Role: e.Role})
		}
		r := &rows[i]
		r.Calls++
		if e.Cost == nil {
			r.Unreported++
			continue
		}
		r.Cost.USD += e.Cost.USD
		r.Cost.InputTokens += e.Cost.InputTokens
		r.Cost.CacheReadInputTokens += e.Cost.CacheReadInputTokens
		r.Cost.CacheCreationInputTokens += e.Cost.CacheCreationInputTokens
		r.Cost.OutputTokens += e.Cost.OutputTokens
		r.Cost.Turns += e.Cost.Turns
	}
	return rows
}

// LedgerSpend reads the slice's ledger and returns its SpendRows; a run that launched no role has no ledger and no rows.
func LedgerSpend(a app.App, slice string) ([]SpendRow, error) {
	b, err := os.ReadFile(filepath.Join(RunDir(a, slice), "ledger.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lg contract.RunLedger
	if err := contract.DecodeStrictBytes(b, &lg); err != nil {
		return nil, err
	}
	return SpendRows(lg.Entries), nil
}
