package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/falsable/a2dlc/internal/app"
)

// journalCommand is `a2dlc journal [--out <file>]`: the HTML page a person or an auditor reads, rendered from the files
// the repo already records (a2dlc-ogj). It reads only. Exit codes: 0 written, 2 a source is unreadable or out of
// contract, or the page cannot be written, 64 bad usage.
func journalCommand(a app.App, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("journal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the page to this file instead of stdout")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 64
	}
	j, err := a.ReadJournal()
	if err != nil {
		fmt.Fprintln(stderr, "a2dlc journal:", err)
		return 2
	}
	b, err := j.HTML()
	if err != nil {
		fmt.Fprintln(stderr, "a2dlc journal:", err)
		return 2
	}
	if *out == "" {
		if _, err := stdout.Write(b); err != nil {
			return 2
		}
		return 0
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(stderr, "a2dlc journal:", err)
		return 2
	}
	return 0
}
