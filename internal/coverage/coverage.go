// Package coverage reads line coverage reports in the two formats almost every toolchain emits
// (Cobertura XML: coverage.py, istanbul/c8, gocover-cobertura, JaCoCo via a converter; LCOV: c8, istanbul, gcov,
// coverage.py). The core knows nothing about languages: only which lines of which file some test run executed.
package coverage

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Report holds, per file (path as the tool wrote it, with /), the statements and their hits, the branch outcomes of
// the lines that have branches, and the branch rate of each class that reports one.
type Report struct {
	files    map[string]map[int]int
	branches map[string]map[int]Branch
	rates    map[string]float64
}

// Branch is how many of the branch outcomes of a line some test took: Taken of Total (Cobertura condition-coverage,
// "50% (1/2)"). Unreadable marks a line the report says has branches but whose condition-coverage does not parse: its
// Taken and Total are zero and mean nothing. A gate that reads branches must treat Unreadable as INCONCLUSIVE, never
// as "no outcome taken".
type Branch struct {
	Taken, Total int
	Unreadable   bool
}

func New() *Report {
	return &Report{files: map[string]map[int]int{}, branches: map[string]map[int]Branch{}, rates: map[string]float64{}}
}

// Parse detects the format from the content.
func Parse(b []byte) (*Report, error) {
	t := bytes.TrimSpace(b)
	switch {
	case len(t) == 0:
		return nil, errors.New("empty coverage report")
	case bytes.HasPrefix(t, []byte("<")):
		return parseCobertura(t)
	case bytes.HasPrefix(t, []byte("TN:")) || bytes.HasPrefix(t, []byte("SF:")):
		return parseLCOV(t)
	}
	return nil, errors.New("neither Cobertura XML nor LCOV")
}

func normalize(file string) string {
	return strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), "./")
}

func (r *Report) add(file string, line, hits int) {
	file = normalize(file)
	m, ok := r.files[file]
	if !ok {
		m = map[int]int{}
		r.files[file] = m
	}
	if h, existed := m[line]; !existed || hits > h {
		m[line] = hits
	}
}

// addBranch keeps the outcome with more branches taken for the line; a readable outcome beats an unreadable one, since
// it is the only one that says anything.
func (r *Report) addBranch(file string, line int, b Branch) {
	file = normalize(file)
	m, ok := r.branches[file]
	if !ok {
		m = map[int]Branch{}
		r.branches[file] = m
	}
	cur, existed := m[line]
	switch {
	case !existed:
		m[line] = b
	case cur.Unreadable != b.Unreadable:
		if !b.Unreadable {
			m[line] = b
		}
	case b.Taken > cur.Taken:
		m[line] = b
	}
}

// addRate keeps the highest branch rate reported for the file.
func (r *Report) addRate(file string, rate float64) {
	file = normalize(file)
	if cur, existed := r.rates[file]; !existed || rate > cur {
		r.rates[file] = rate
	}
}

// Merge adds another report: a line executed in either of the two stays executed, and a branch taken in either stays
// taken.
func (r *Report) Merge(o *Report) {
	if o == nil {
		return
	}
	for f, m := range o.files {
		for l, h := range m {
			r.add(f, l, h)
		}
	}
	for f, m := range o.branches {
		for l, b := range m {
			r.addBranch(f, l, b)
		}
	}
	for f, rate := range o.rates {
		r.addRate(f, rate)
	}
}

// matchKey finds the key of path in m: an exact match; or a key that ends in "/<path>" (absolute paths or paths
// relative to another root); or, the other way around, a key relative to a repo subfolder that path ends in (filename
// "db/x.py" for "django/db/x.py"), the longest one. "" if none matches.
func matchKey[V any](path string, m map[string]V) string {
	if _, ok := m[path]; ok {
		return path
	}
	best := ""
	for f := range m {
		if strings.HasSuffix(f, "/"+path) && (best == "" || len(f) < len(best)) {
			best = f
		}
	}
	if best == "" {
		for f := range m {
			if !strings.HasPrefix(f, "/") && strings.HasSuffix(path, "/"+f) && len(f) > len(best) {
				best = f
			}
		}
	}
	return best
}

// Lookup finds the repo file (path relative, with /) in the report, with the rules of matchKey. nil if not found.
func (r *Report) Lookup(path string) map[int]int {
	k := matchKey(path, r.files)
	if k == "" {
		return nil
	}
	return r.files[k]
}

// LookupBranches returns the branch outcomes per line of the file, found as Lookup finds it. nil means the report gives
// no branch data for the file: it is "not reported", never "no branches", and a gate that needs branches must read nil
// as INCONCLUSIVE. Nothing consumes it yet (the trace gate of stage15 is not wired to it).
func (r *Report) LookupBranches(path string) map[int]Branch {
	k := matchKey(path, r.branches)
	if k == "" {
		return nil
	}
	return r.branches[k]
}

// LookupBranchRate returns the branch rate that the report gives the file's class, if it gives one. A rate that does
// not parse is not reported (false): the report stays readable for line coverage, and a gate that needs the rate
// treats its absence as INCONCLUSIVE. Nothing consumes it yet.
func (r *Report) LookupBranchRate(path string) (float64, bool) {
	k := matchKey(path, r.rates)
	if k == "" {
		return 0, false
	}
	return r.rates[k], true
}

// Files returns how many files the report has.
func (r *Report) Files() int { return len(r.files) }

type xcov struct {
	Sources  []string `xml:"sources>source"`
	Packages []struct {
		Classes []struct {
			Filename   string `xml:"filename,attr"`
			BranchRate string `xml:"branch-rate,attr"`
			Lines      []struct {
				Number    int    `xml:"number,attr"`
				Hits      int    `xml:"hits,attr"`
				Branch    string `xml:"branch,attr"`
				Condition string `xml:"condition-coverage,attr"`
			} `xml:"lines>line"`
		} `xml:"classes>class"`
	} `xml:"packages>package"`
}

// conditionRe reads Cobertura's condition-coverage of a line: "50% (1/2)" is one of two branch outcomes taken.
var conditionRe = regexp.MustCompile(`\((\d+)/(\d+)\)`)

func parseCobertura(b []byte) (*Report, error) {
	var x xcov
	if err := xml.Unmarshal(b, &x); err != nil {
		return nil, fmt.Errorf("unreadable Cobertura XML: %v", err)
	}
	r := New()
	var sources []string
	for _, s := range x.Sources {
		if s = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/"), "/"); s != "" {
			sources = append(sources, s)
		}
	}
	for _, p := range x.Packages {
		for _, c := range p.Classes {
			f := strings.ReplaceAll(c.Filename, "\\", "/")
			// Tools write filename relative to <source>. With a single absolute source, the absolute path
			// is unambiguous and Lookup finds it by suffix even when the source is a subfolder of the repo
			// (coverage run --source=/testbed/django gives filename "db/x.py"); an absolute filename is left as is.
			if len(sources) == 1 && strings.HasPrefix(sources[0], "/") && !strings.HasPrefix(f, "/") {
				f = sources[0] + "/" + f
			}
			for _, l := range c.Lines {
				r.add(f, l.Number, l.Hits)
				if l.Branch != "true" {
					continue
				}
				if m := conditionRe.FindStringSubmatch(l.Condition); m != nil {
					taken, _ := strconv.Atoi(m[1])
					total, _ := strconv.Atoi(m[2])
					r.addBranch(f, l.Number, Branch{Taken: taken, Total: total})
				} else {
					r.addBranch(f, l.Number, Branch{Unreadable: true})
				}
			}
			if rate, err := strconv.ParseFloat(strings.TrimSpace(c.BranchRate), 64); err == nil && c.BranchRate != "" {
				r.addRate(f, rate)
			}
		}
	}
	return r, nil
}

func parseLCOV(b []byte) (*Report, error) {
	r := New()
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	file := ""
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "SF:"):
			file = strings.TrimSpace(l[3:])
		case strings.HasPrefix(l, "DA:") && file != "":
			fields := strings.Split(l[3:], ",")
			if len(fields) < 2 {
				continue
			}
			n, err1 := strconv.Atoi(strings.TrimSpace(fields[0]))
			h, err2 := strconv.Atoi(strings.TrimSpace(fields[1]))
			if err1 == nil && err2 == nil {
				r.add(file, n, h)
			}
		case l == "end_of_record":
			file = ""
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return r, nil
}
