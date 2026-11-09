package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"

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

// HTML renders the journal with a fixed layout: the same files give the same bytes (no clock, no host path). Every
// recorded value goes through html.EscapeString and lands only in a text node or a double-quoted attribute, which is all
// the escaping this page needs; html/template is not used because govulncheck reaches its open advisories on Go 1.25.
func (j Journal) HTML() ([]byte, error) {
	e := html.EscapeString
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	b.WriteString(journalHead)
	b.WriteString("<h2>Where it stands</h2>\n")
	if v := j.Verdict; v != nil {
		w("<p>The last check says <span class=\"v %s\">%s</span> for tree <code>%s</code>, compared against <code>%s</code>, on %s.</p>\n",
			e(v.Verdict), e(v.Verdict), e(v.TreeHash), e(v.BaseRef), e(v.CreatedAt))
	} else {
		b.WriteString("<p>No check has written a receipt yet (<code>.a2dlc/verdict.json</code> is missing).</p>\n")
	}

	b.WriteString("\n<h2>Waiting on a person</h2>\n")
	if len(j.Escalations) == 0 && len(j.PendingWaivers) == 0 {
		b.WriteString("<p>Nothing.</p>\n")
	}
	for _, x := range j.Escalations {
		w("<div class=\"card\"><strong>%s</strong><br><span class=\"muted\">%s · <code>%s</code>", e(x.Summary), e(x.Kind), e(x.Target))
		if len(x.ResolveWith) > 0 {
			w(" · resolve with: %s", e(strings.Join(x.ResolveWith, ", ")))
		}
		b.WriteString("</span></div>\n")
	}
	for _, t := range j.PendingWaivers {
		w("<div class=\"card\">Waiver for <code>%s</code> needs a signature (<code>a2dlc approve --pending</code>).</div>\n", e(t))
	}

	b.WriteString("\n<h2>Behavior changes and unjudged code</h2>\n")
	if len(j.Changes) == 0 {
		b.WriteString("<p>None in the last receipt.</p>\n")
	} else {
		b.WriteString("<table><tr><th>Target</th><th>Result</th><th>Decision</th></tr>\n")
		for _, c := range j.Changes {
			w("<tr><td><code>%s</code></td><td><span class=\"v %s\">%s</span>", e(c.Target), e(c.Verdict), e(c.Verdict))
			if c.Note != "" {
				w(" <span class=\"muted\">(%s)</span>", e(c.Note))
			}
			if c.Reason != "" {
				w("<br><span class=\"muted\">%s</span>", e(c.Reason))
			}
			b.WriteString("</td><td>")
			switch {
			case c.Decision == "":
				b.WriteString("<span class=\"muted\">no waiver</span>")
			default:
				b.WriteString(e(c.Decision))
				if c.Why != "" {
					w(": %s", e(c.Why))
				}
				if c.Principal != "" {
					w("<br><span class=\"muted\">signed by %s</span>", e(c.Principal))
				} else {
					b.WriteString("<br><span class=\"muted\">not signed</span>")
				}
			}
			b.WriteString("</td></tr>\n")
		}
		b.WriteString("</table>\n")
	}

	b.WriteString("\n<h2>Open findings</h2>\n")
	if len(j.OpenFindings) == 0 {
		b.WriteString("<p>None.</p>\n")
	} else {
		b.WriteString("<ul>")
		for _, f := range j.OpenFindings {
			w("<li><code>%s</code> %s on <code>%s</code> <span class=\"muted\">from %s, %s since %s</span></li>\n",
				e(f.ID), e(f.Kind), e(f.Target), e(f.Source), e(f.Outcome), e(f.RaisedAt))
		}
		b.WriteString("</ul>\n")
	}
	if len(j.Report.Sources) > 0 {
		b.WriteString("<table><tr><th>Raised by</th><th>Raised</th><th>Fixed</th><th>Intentional</th><th>False</th><th>Irrelevant</th><th>Open</th></tr>\n")
		for _, s := range j.Report.Sources {
			w("<tr><td><code>%s</code></td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>\n",
				e(s.Source), s.Raised, s.Fixed, s.Intentional, s.False, s.Irrelevant, s.Open)
		}
		b.WriteString("</table>\n")
	}

	b.WriteString("\n<h2>Sources</h2>\n<table><tr><th>File</th><th>sha256</th></tr>\n")
	for _, s := range j.Sources {
		hash := "<span class=\"muted\">missing</span>"
		if s.SHA256 != "" {
			hash = "<code>" + e(s.SHA256) + "</code>"
		}
		w("<tr><td><code>%s</code></td><td>%s</td></tr>\n", e(s.Path), hash)
	}
	b.WriteString("</table>\n</main></body>\n</html>\n")
	return []byte(b.String()), nil
}

const journalHead = `<!doctype html>
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

`
