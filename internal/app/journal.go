package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"os"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
)

// JournalSource is one file the journal was rendered from, with the sha256 of its bytes: an auditor regenerates the page
// from the same files and compares, instead of trusting the HTML.
type JournalSource struct {
	Path   string
	SHA256 string // empty: the file does not exist
}

// JournalChange is a target of the receipt whose behavior changed or could not be judged, with the waiver and approval
// that cover it, if any.
type JournalChange struct {
	Target    string
	Verdict   string
	Note      string
	Reason    string
	Waived    bool
	Decision  string
	Why       string
	Principal string
}

// Journal is what `a2dlc journal` renders (a2dlc-ogj, first step): only what the repo already records, no new events
// and no model. Every field comes from a file in Sources.
type Journal struct {
	Verdict        *contract.Verdict
	Escalations    []contract.Escalation
	PendingWaivers []string
	Changes        []JournalChange
	OpenFindings   []FindingRow
	Report         FindingReport
	Sources        []JournalSource
}

// readSource decodes rel (strict, as every reader in this package) and records its hash; a missing file is a source with
// no hash and leaves v as it was. A file out of contract is an error: the page never renders it as if it were fine.
func (a App) readSource(rel string, v any, srcs *[]JournalSource) (bool, error) {
	b, err := os.ReadFile(a.path(rel))
	if errors.Is(err, os.ErrNotExist) {
		*srcs = append(*srcs, JournalSource{Path: rel})
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(b)
	*srcs = append(*srcs, JournalSource{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	if v == nil {
		return true, nil
	}
	if err := contract.DecodeStrictBytes(b, v); err != nil {
		return false, fmt.Errorf("%s is out of contract: %w", rel, err)
	}
	return true, nil
}

// ReadJournal joins the receipt, the escalations, the waivers, the approvals and the findings record of the working tree.
func (a App) ReadJournal() (Journal, error) {
	var j Journal
	var v contract.Verdict
	ok, err := a.readSource(pathVerdict, &v, &j.Sources)
	if err != nil {
		return j, err
	}
	if ok {
		j.Verdict = &v
		j.PendingWaivers = v.PendingWaivers
	}
	var esc contract.Escalations
	if _, err := a.readSource(pathEscalations, &esc, &j.Sources); err != nil {
		return j, err
	}
	j.Escalations = esc.Items
	var w contract.Waivers
	if _, err := a.readSource(pathWaivers, &w, &j.Sources); err != nil {
		return j, err
	}
	var ap contract.Approvals
	if _, err := a.readSource(pathApprovals, &ap, &j.Sources); err != nil {
		return j, err
	}
	if _, err := a.readSource(pathFindings, nil, &j.Sources); err != nil {
		return j, err
	}
	if j.OpenFindings, err = a.FindingList(true); err != nil {
		return j, err
	}
	if j.Report, err = a.FindingReport(); err != nil {
		return j, err
	}
	if j.Verdict != nil {
		waivers := map[string]contract.Waiver{}
		for _, x := range w.Waivers {
			waivers[x.Target] = x
		}
		signedBy := map[string]string{}
		for _, x := range ap.Approvals {
			signedBy[x.Target] = x.Principal
		}
		for _, t := range j.Verdict.Targets {
			if t.Verdict == "PASS" && !t.Waived && t.Note == "" {
				continue
			}
			c := JournalChange{Target: t.Target, Verdict: t.Verdict, Note: t.Note, Reason: t.Reason, Waived: t.Waived, Principal: signedBy[t.Target]}
			if x, ok := waivers[t.Target]; ok {
				c.Decision, c.Why = x.Decision, x.Reason
			}
			j.Changes = append(j.Changes, c)
		}
		sort.SliceStable(j.Changes, func(i, k int) bool { return j.Changes[i].Target < j.Changes[k].Target })
	}
	return j, nil
}

// HTML renders the journal with a fixed template: the same files give the same bytes (no clock, no host path).
func (j Journal) HTML() ([]byte, error) {
	var b bytes.Buffer
	if err := journalTemplate.Execute(&b, j); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

var journalTemplate = template.Must(template.New("journal").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>a2dlc journal</title>
<style>
:root{--bg:#fbfaf7;--fg:#1d1d1b;--muted:#6b6a65;--line:#e3e0d8;--pass:#1f7a4d;--fail:#b3261e;--inc:#8a5a00;--card:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#161614;--fg:#ecebe6;--muted:#a3a29b;--line:#2f2e2a;--pass:#5cc08c;--fail:#ff8a80;--inc:#e0b252;--card:#1e1e1b}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:860px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:17px;margin:32px 0 8px;padding-bottom:4px;border-bottom:1px solid var(--line)}
.muted{color:var(--muted)}code{font:13px ui-monospace,Consolas,monospace;word-break:break-all}
.v{font-weight:600}.PASS{color:var(--pass)}.FAIL{color:var(--fail)}.INCONCLUSIVE{color:var(--inc)}
ul{padding-left:20px}li{margin:6px 0}
table{border-collapse:collapse;width:100%;font-size:14px}th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}
.card{background:var(--card);border:1px solid var(--line);border-radius:6px;padding:12px 14px;margin:8px 0}
</style>
</head>
<body><main>
<h1>Development journal</h1>
<p class="muted">Rendered by a2dlc from the files listed at the end, with no model. Signatures are shown, not verified: <code>a2dlc verify</code> checks them.</p>

<h2>Where it stands</h2>
{{with .Verdict}}<p>The last check says <span class="v {{.Verdict}}">{{.Verdict}}</span> for tree <code>{{.TreeHash}}</code>, compared against <code>{{.BaseRef}}</code>, on {{.CreatedAt}}.</p>
{{else}}<p>No check has written a receipt yet (<code>.a2dlc/verdict.json</code> is missing).</p>{{end}}

<h2>Waiting on a person</h2>
{{if or .Escalations .PendingWaivers}}
{{range .Escalations}}<div class="card"><strong>{{.Summary}}</strong><br><span class="muted">{{.Kind}} · <code>{{.Target}}</code>{{if .ResolveWith}} · resolve with: {{range $i, $r := .ResolveWith}}{{if $i}}, {{end}}{{$r}}{{end}}{{end}}</span></div>
{{end}}{{range .PendingWaivers}}<div class="card">Waiver for <code>{{.}}</code> needs a signature (<code>a2dlc approve --pending</code>).</div>
{{end}}{{else}}<p>Nothing.</p>{{end}}

<h2>Behavior changes and unjudged code</h2>
{{if .Changes}}<table><tr><th>Target</th><th>Result</th><th>Decision</th></tr>
{{range .Changes}}<tr><td><code>{{.Target}}</code></td><td><span class="v {{.Verdict}}">{{.Verdict}}</span>{{if .Note}} <span class="muted">({{.Note}})</span>{{end}}{{if .Reason}}<br><span class="muted">{{.Reason}}</span>{{end}}</td>
<td>{{if .Decision}}{{.Decision}}{{if .Why}}: {{.Why}}{{end}}{{if .Principal}}<br><span class="muted">signed by {{.Principal}}</span>{{else}}<br><span class="muted">not signed</span>{{end}}{{else}}<span class="muted">no waiver</span>{{end}}</td></tr>
{{end}}</table>{{else}}<p>None in the last receipt.</p>{{end}}

<h2>Open findings</h2>
{{if .OpenFindings}}<ul>{{range .OpenFindings}}<li><code>{{.ID}}</code> {{.Kind}} on <code>{{.Target}}</code> <span class="muted">from {{.Source}}, {{.Outcome}} since {{.RaisedAt}}</span></li>
{{end}}</ul>{{else}}<p>None.</p>{{end}}
{{if .Report.Sources}}<table><tr><th>Raised by</th><th>Raised</th><th>Fixed</th><th>Intentional</th><th>False</th><th>Irrelevant</th><th>Open</th></tr>
{{range .Report.Sources}}<tr><td><code>{{.Source}}</code></td><td>{{.Raised}}</td><td>{{.Fixed}}</td><td>{{.Intentional}}</td><td>{{.False}}</td><td>{{.Irrelevant}}</td><td>{{.Open}}</td></tr>
{{end}}</table>{{end}}

<h2>Sources</h2>
<table><tr><th>File</th><th>sha256</th></tr>
{{range .Sources}}<tr><td><code>{{.Path}}</code></td><td>{{if .SHA256}}<code>{{.SHA256}}</code>{{else}}<span class="muted">missing</span>{{end}}</td></tr>
{{end}}</table>
</main></body>
</html>
`))
