package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/falsable/a2dlc/internal/app"
)

// doctorCommand is `a2dlc doctor [--json]`: 0 when nothing is blocking, 1 when something is, 2 when doctor itself cannot
// run, 64 on bad usage. --json prints the report of contracts/doctor.schema.json, the same text the MCP tool returns.
func doctorCommand(ctx context.Context, a app.App, exe string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print every finding as JSON, for an agent")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return 64
	}
	d, err := a.Doctor(ctx, exe)
	if err != nil {
		fmt.Fprintln(stderr, "a2dlc doctor:", err)
		return 2
	}
	if *asJSON {
		fmt.Fprintln(stdout, app.DoctorJSON(d))
	} else {
		fmt.Fprint(stdout, app.DoctorText(d))
	}
	if d.Blocking() {
		return 1
	}
	return 0
}
