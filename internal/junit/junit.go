// Package junit reads JUnit XML, the common format of test runners (pytest, vitest, jest, gotestsum…): that way the
// test gates know nothing about languages.
package junit

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// Case is a testcase of one run.
type Case struct {
	File      string
	Classname string
	Name      string
	Status    string // contract.TestPassed | TestFailed | TestError | TestSkipped
	Message   string
}

// ID identifies the testcase across runs: <file>::<classname>::<name> if the runner gives the file (pytest with
// junit_family=xunit1), <classname>::<name> if not (vitest puts the file as classname), and junit::<name> with neither
// of the two (a collection error). Only if what comes before :: is a file can a waiver be signed.
func (c Case) ID() string {
	switch {
	case c.File != "":
		return c.File + "::" + c.Classname + "::" + c.Name
	case c.Classname != "":
		return c.Classname + "::" + c.Name
	}
	return "junit::" + c.Name
}

type xmsg struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type xcase struct {
	Classname string    `xml:"classname,attr"`
	Name      string    `xml:"name,attr"`
	File      string    `xml:"file,attr"`
	Failure   []xmsg    `xml:"failure"`
	Error     []xmsg    `xml:"error"`
	Skipped   *struct{} `xml:"skipped"`
}

// worst orders the statuses: if an id appears twice, the worst one stays.
var worst = map[string]int{contract.TestSkipped: 0, contract.TestPassed: 1, contract.TestFailed: 2, contract.TestError: 3}

// Parse returns the testcases by id. It accepts <testsuites> or <testsuite> as the root, nested at any level.
func Parse(b []byte) (map[string]Case, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	out := map[string]Case{}
	root := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "testsuites", "testsuite":
			root = true
		case "testcase":
			var x xcase
			if err := d.DecodeElement(&x, &se); err != nil {
				return nil, err
			}
			c := Case{File: strings.TrimPrefix(strings.ReplaceAll(x.File, "\\", "/"), "./"), Classname: x.Classname, Name: x.Name, Status: contract.TestPassed}
			switch {
			case len(x.Error) > 0:
				c.Status, c.Message = contract.TestError, message(x.Error[0])
			case len(x.Failure) > 0:
				c.Status, c.Message = contract.TestFailed, message(x.Failure[0])
			case x.Skipped != nil:
				c.Status = contract.TestSkipped
			}
			if v, existed := out[c.ID()]; !existed || worst[c.Status] > worst[v.Status] {
				out[c.ID()] = c
			}
		}
	}
	if !root {
		return nil, errors.New("not JUnit XML: <testsuites> or <testsuite> is missing")
	}
	return out, nil
}

// message is the type and message of the failure, or the first line of the text if the runner does not give them. Trimmed.
func message(m xmsg) string {
	s := strings.TrimSpace(m.Message)
	if t := strings.TrimSpace(m.Type); t != "" && !strings.HasPrefix(s, t) {
		s = strings.TrimSpace(t + ": " + s)
	}
	if s == "" {
		for _, l := range strings.Split(m.Text, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				s = l
				break
			}
		}
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Red says whether a status counts as a failed test.
func Red(status string) bool { return status == contract.TestFailed || status == contract.TestError }
