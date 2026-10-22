package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// answerCommand is `a2dlc answer --slice <id> --probe <id> --choice <c> --reason <text> --by <name>`: a person's answer to a
// probe the run asked (spec_divergence in Stage 1, pi_S relaxations in Stage 1.5). It writes answers.json in the run's folder
// (a2dlc run resumes it). Roles never run this command. Exit codes: 0 written, 64 bad usage, 2 the run or the probe is not
// answerable.
func answerCommand(a app.App, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slice := fs.String("slice", "", "slice id of the run")
	probe := fs.String("probe", "", "probe id (the escalation id for spec_divergence)")
	choice := fs.String("choice", "", "the option chosen among the probe's options")
	reason := fs.String("reason", "", "why: written down, shown on resume")
	by := fs.String("by", "", "who answers: the name the operator gives (recorded, not authenticated)")
	if fs.Parse(args) != nil || *slice == "" || *probe == "" || *choice == "" || *reason == "" || *by == "" {
		fmt.Fprintln(stderr, "a2dlc answer: needs --slice, --probe, --choice, --reason and --by")
		return 64
	}
	path, err := a2run.WriteAnswer(a, *slice, contract.Answer{ProbeID: *probe, Choice: *choice, Reason: *reason, By: *by})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(os.Stdout, "a2dlc answer: recorded in", path)
	return 0
}
