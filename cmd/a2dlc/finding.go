package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// findingCommand is `a2dlc finding report|list|label`: the record of what happened to each finding (a2dlc-277).
// report and list only read it. label is the one way a finding becomes false or irrelevant; it records who set it.
// Exit codes of label: 0 written, 1 no such finding, 2 the record cannot be read or written, 64 bad usage.
func findingCommand(a app.App, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "a2dlc finding: report, list or label")
		return 64
	}
	fs := flag.NewFlagSet("finding "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	switch args[0] {
	case "report":
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		r, err := a.FindingReport()
		if err != nil {
			fmt.Fprintln(stderr, "a2dlc finding report:", err)
			return 2
		}
		if *asJSON {
			return printJSON(stdout, stderr, r)
		}
		fmt.Fprint(stdout, r.Text())
		return 0
	case "list":
		openOnly := fs.Bool("open", false, "only findings with no outcome yet (open or parked)")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		rows, err := a.FindingList(*openOnly)
		if err != nil {
			fmt.Fprintln(stderr, "a2dlc finding list:", err)
			return 2
		}
		if *asJSON {
			return printJSON(stdout, stderr, rows)
		}
		for _, r := range rows {
			fmt.Fprintf(stdout, "%s  %-11s  %s  %s  %s\n", r.ID, r.Outcome, r.Source, r.Kind, r.Target)
		}
		return 0
	case "label":
		id := fs.String("id", "", "finding id (a2dlc finding list)")
		outcome := fs.String("as", "", "false or irrelevant")
		by := fs.String("by", "", "who sets it: a person's name or the agent's (recorded, not authenticated)")
		reason := fs.String("reason", "", "why (optional)")
		if fs.Parse(args[1:]) != nil || *id == "" || *outcome == "" || *by == "" {
			fmt.Fprintln(stderr, "a2dlc finding label: needs --id, --as and --by")
			return 64
		}
		e, err := a.LabelFinding(*id, *outcome, *by, contract.ViaCLI, *reason)
		switch {
		case errors.Is(err, app.ErrInvalidLabel):
			fmt.Fprintln(stderr, "a2dlc finding label:", err)
			return 64
		case errors.Is(err, app.ErrUnknownFinding):
			fmt.Fprintln(stderr, "a2dlc finding label:", err)
			return 1
		case err != nil:
			fmt.Fprintln(stderr, "a2dlc finding label:", err)
			return 2
		}
		if *asJSON {
			return printJSON(stdout, stderr, e)
		}
		fmt.Fprintf(stdout, "a2dlc finding label: %s is %s (by %s)\n", e.ID, e.Outcome, e.By)
		return 0
	}
	fmt.Fprintln(stderr, "a2dlc finding: report, list or label")
	return 64
}

func printJSON(stdout, stderr io.Writer, v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "a2dlc finding:", err)
		return 2
	}
	fmt.Fprintln(stdout, string(b))
	return 0
}
