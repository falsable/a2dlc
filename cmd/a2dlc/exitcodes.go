package main

// exitRow is one exit code of one command, with what it means. exitTable is the single list of them: docs/COMPATIBILITY.md
// prints it and exitcodes_test.go fails when a command returns a code the table does not list under it, when the table
// lists a code no command returns, or when the document drifts from the table. An agent that drives a2dlc branches on
// these numbers, so they are part of the contract. Command is the first word of the command line, and the test compares
// the codes per command, not per subcommand (shortcut: one row per command, with the subcommands told apart in Meaning).
type exitRow struct {
	Command string
	Code    int
	Meaning string
}

var exitTable = []exitRow{
	{"version", 0, "Printed the version."},
	{"usage", 64, "No command, an unknown command, or the working directory is not a git repository (hook allows instead)."},
	{"check", 0, "PASS, or --level 0 (informs, never blocks)."},
	{"check", 1, "FAIL, or --require-signatures found a waiver without a valid signature."},
	{"check", 2, "INCONCLUSIVE, or a2dlc itself failed."},
	{"check", 64, "Bad usage: unknown flag, --level 0 combined with other flags, or --level not 0 or 1."},
	{"run", 0, "PASS."},
	{"run", 1, "FAIL."},
	{"run", 2, "INCONCLUSIVE, escalated to a person, or a2dlc itself failed."},
	{"run", 64, "Bad usage: missing --slice or unknown flag."},
	{"verify-run", 0, "The ledger chain and the run's artifacts recalculate."},
	{"verify-run", 1, "A problem was found, or the run could not be read."},
	{"verify-run", 64, "Bad usage: missing --slice or unknown flag."},
	{"verify", 0, "The commit may be pushed (warnings go to stderr)."},
	{"verify", 1, "Blocked by evidence, or --strict found anything but a PASS receipt for the tree, or a2dlc or git failed."},
	{"verify", 64, "Bad usage: unknown flag."},
	{"approve", 0, "Signed (approve --pending: listed, nothing to sign, or signed)."},
	{"approve", 1, "Not signed: the signing failed, or approve --pending was not confirmed."},
	{"approve", 64, "Bad usage: missing target, --key or --principal."},
	{"authorize", 0, "Signed."},
	{"authorize", 1, "Not signed: the request was refused or the signing failed."},
	{"authorize", 64, "Bad usage: missing --key or --principal, or unknown flag."},
	{"plan", 0, "plan check: valid. plan run: PASS. propose, replan, import: written."},
	{"plan", 1, "plan check: not valid. plan run: the plan was refused. propose, replan, import: not written."},
	{"plan", 2, "plan run: escalated. Any subcommand: a2dlc itself failed."},
	{"plan", 64, "Bad usage: missing or unknown subcommand or flag, or an unreadable plan or intent file."},
	{"propose", 0, "The role answered (status ok). --from-escalations: done, also with nothing new to propose."},
	{"propose", 1, "The role did not give an answer that holds (status is not ok)."},
	{"propose", 2, "a2dlc itself failed (--from-escalations: an unreadable escalations.json, waivers.json or record)."},
	{"propose", 64, "Bad usage: missing --out or unknown flag, or --from-escalations with --base."},
	{"answer", 0, "The answer was recorded."},
	{"answer", 2, "The run or the probe is not answerable."},
	{"answer", 64, "Bad usage: a flag is missing."},
	{"finding", 0, "Done."},
	{"finding", 1, "label: no such finding."},
	{"finding", 2, "The record is unreadable."},
	{"finding", 64, "Bad usage."},
	{"hook", 0, "Allowed, or informed in the hook's JSON answer (a Stop that blocks does so in that answer, not in the code)."},
	{"hook", 2, "PreToolUse blocked the tool call: it edits a protected path without a signed approval, or it is git push --no-verify."},
	{"hook", 64, "Bad usage: the agent is not claude or gemini."},
	{"install", 0, "Installed."},
	{"install", 1, "Not installed."},
	{"install", 64, "Bad usage: unknown flag."},
	{"doctor", 0, "No blocking finding."},
	{"doctor", 1, "At least one blocking finding."},
	{"doctor", 2, "doctor itself failed."},
	{"doctor", 64, "Bad usage: unknown flag."},
	{"mcp", 0, "The client closed the connection."},
	{"mcp", 1, "The server failed."},
}
