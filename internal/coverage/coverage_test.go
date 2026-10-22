package coverage

import "testing"

const cobertura = `<?xml version="1.0" ?>
<coverage version="7.6.1" line-rate="0.5">
 <sources><source>/testbed</source></sources>
 <packages><package name="django.db">
  <classes>
   <class name="fields.py" filename="django/db/fields.py" line-rate="0.5">
    <methods/>
    <lines><line number="1" hits="1"/><line number="7" hits="0"/><line number="9" hits="3"/></lines>
   </class>
   <class name="abs.py" filename="/testbed/django/abs.py"><lines><line number="2" hits="1"/></lines></class>
  </classes>
 </package></packages>
</coverage>`

const lcov = "TN:\nSF:/work/src/lib/f.ts\nDA:1,1\nDA:2,0\nDA:5,2\nend_of_record\nSF:src/lib/g.ts\nDA:3,1\nend_of_record\n"

func TestParseCoberturaYLCOV(t *testing.T) {
	c, err := Parse([]byte(cobertura))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Lookup("django/db/fields.py")
	if m == nil || m[1] != 1 || m[7] != 0 || m[9] != 3 || len(m) != 3 {
		t.Fatalf("%v", m)
	}
	if c.Lookup("django/abs.py") == nil {
		t.Fatal("an absolute filename was not found by suffix")
	}
	if c.Lookup("other.py") != nil {
		t.Fatal("an absent file returned something")
	}
	// coverage run --source=/testbed/django writes <source>/testbed/django</source> and filename "db/x.py"
	sub, _ := Parse([]byte(`<coverage><sources><source>/testbed/django</source></sources><packages><package><classes>
	 <class filename="db/x.py"><lines><line number="4" hits="1"/></lines></class></classes></package></packages></coverage>`))
	if m := sub.Lookup("django/db/x.py"); m == nil || m[4] != 1 {
		t.Fatalf("subfolder as source: %v", m)
	}
	// and without <source>, the path relative to the subfolder is found as a suffix of the repo path
	rel, _ := Parse([]byte(`<coverage><packages><package><classes><class filename="db/x.py"><lines><line number="4" hits="1"/></lines></class></classes></package></packages></coverage>`))
	if m := rel.Lookup("django/db/x.py"); m == nil || m[4] != 1 {
		t.Fatalf("path relative to subfolder: %v", m)
	}
	l, err := Parse([]byte(lcov))
	if err != nil {
		t.Fatal(err)
	}
	// an absolute path in SF: is found by suffix
	if m := l.Lookup("src/lib/f.ts"); m == nil || m[5] != 2 || m[2] != 0 {
		t.Fatalf("%v", m)
	}
	if m := l.Lookup("src/lib/g.ts"); m == nil || m[3] != 1 {
		t.Fatalf("%v", m)
	}
	// a suffix that does not start at a separator does not count
	if l.Lookup("lib/f.ts") == nil || l.Lookup("ib/f.ts") != nil {
		t.Fatal("the suffix must start at /")
	}
}

// Cobertura branch data: a line with branch="true" and condition-coverage gives taken/total outcomes, and a class
// branch-rate gives the file's rate; a line without branch data gives none.
const coberturaBranches = `<coverage line-rate="0.5" branch-rate="0.25"><packages><package><classes>
 <class filename="pkg/b.py" branch-rate="0.25"><lines>
  <line number="3" hits="2" branch="true" condition-coverage="50% (1/2)"/>
  <line number="4" hits="0" branch="true" condition-coverage="0% (0/2)"/>
  <line number="5" hits="1"/>
 </lines></class>
 <class filename="pkg/c.py"><lines><line number="1" hits="1"/></lines></class>
</classes></package></packages></coverage>`

func TestCoberturaBranches(t *testing.T) {
	c, err := Parse([]byte(coberturaBranches))
	if err != nil {
		t.Fatal(err)
	}
	br := c.LookupBranches("pkg/b.py")
	if br[3] != (Branch{Taken: 1, Total: 2}) || br[4] != (Branch{Taken: 0, Total: 2}) || len(br) != 2 {
		t.Fatalf("branches: %v", br)
	}
	if rate, ok := c.LookupBranchRate("pkg/b.py"); !ok || rate != 0.25 {
		t.Fatalf("branch-rate: %v %v", rate, ok)
	}
	if _, ok := c.LookupBranchRate("pkg/c.py"); ok {
		t.Fatal("a class without branch-rate reported one")
	}
	if c.LookupBranches("pkg/c.py") != nil {
		t.Fatal("a class without branch data reported branches")
	}
	if m := c.Lookup("pkg/b.py"); m == nil || m[5] != 1 {
		t.Fatalf("line hits: %v", m)
	}
}

// A branch-rate that does not parse is not reported: the report stays readable for line coverage, and the rate lookup
// says so instead of returning a number.
func TestUnreadableBranchRateIsNotReported(t *testing.T) {
	bad, err := Parse([]byte(`<coverage><packages><package><classes><class filename="x.py" branch-rate="high"><lines><line number="2" hits="1"/></lines></class></classes></package></packages></coverage>`))
	if err != nil {
		t.Fatalf("an unreadable branch-rate must not make the report unreadable: %v", err)
	}
	if _, ok := bad.LookupBranchRate("x.py"); ok {
		t.Fatal("an unreadable branch-rate was reported")
	}
	if m := bad.Lookup("x.py"); m == nil || m[2] != 1 {
		t.Fatalf("line coverage lost with the rate: %v", m)
	}
}

// A line that claims branches with a condition-coverage that does not parse is kept as Unreadable, never dropped: a
// gate reading the branches must not take its absence for "no branches".
func TestUnreadableConditionCoverageIsKept(t *testing.T) {
	c, err := Parse([]byte(`<coverage><packages><package><classes><class filename="pkg/d.py"><lines>
	 <line number="6" hits="1" branch="true" condition-coverage="half"/>
	 <line number="7" hits="1" branch="true" condition-coverage="100% (2/2)"/>
	</lines></class></classes></package></packages></coverage>`))
	if err != nil {
		t.Fatal(err)
	}
	br := c.LookupBranches("pkg/d.py")
	if len(br) != 2 || !br[6].Unreadable || br[6].Taken != 0 || br[7] != (Branch{Taken: 2, Total: 2}) {
		t.Fatalf("branches: %+v", br)
	}
	if m := c.Lookup("pkg/d.py"); m == nil || m[6] != 1 {
		t.Fatalf("the line's hits are lost: %v", m)
	}
}

// Across reports a readable outcome beats an unreadable one, whichever comes first.
func TestMergeReadableBranchBeatsUnreadable(t *testing.T) {
	bad, _ := Parse([]byte(`<coverage><packages><package><classes><class filename="pkg/e.py"><lines>
	 <line number="3" hits="1" branch="true" condition-coverage="?"/></lines></class></classes></package></packages></coverage>`))
	good, _ := Parse([]byte(`<coverage><packages><package><classes><class filename="pkg/e.py"><lines>
	 <line number="3" hits="1" branch="true" condition-coverage="50% (1/2)"/></lines></class></classes></package></packages></coverage>`))
	bad.Merge(good)
	if br := bad.LookupBranches("pkg/e.py"); br[3] != (Branch{Taken: 1, Total: 2}) {
		t.Fatalf("readable outcome lost: %+v", br)
	}
	good2, _ := Parse([]byte(`<coverage><packages><package><classes><class filename="pkg/e.py"><lines>
	 <line number="3" hits="1" branch="true" condition-coverage="?"/></lines></class></classes></package></packages></coverage>`))
	good.Merge(good2)
	if br := good.LookupBranches("pkg/e.py"); br[3] != (Branch{Taken: 1, Total: 2}) {
		t.Fatalf("an unreadable outcome replaced a readable one: %+v", br)
	}
}

func TestMergeKeepsMostBranchesTaken(t *testing.T) {
	a, _ := Parse([]byte(coberturaBranches))
	b, _ := Parse([]byte(`<coverage><packages><package><classes>
	 <class filename="pkg/b.py" branch-rate="0.75"><lines>
	  <line number="3" hits="2" branch="true" condition-coverage="100% (2/2)"/>
	  <line number="4" hits="0" branch="true" condition-coverage="0% (0/2)"/>
	 </lines></class></classes></package></packages></coverage>`))
	a.Merge(b)
	if br := a.LookupBranches("pkg/b.py"); br[3] != (Branch{Taken: 2, Total: 2}) || br[4] != (Branch{Taken: 0, Total: 2}) {
		t.Fatalf("merged branches: %v", br)
	}
	if rate, _ := a.LookupBranchRate("pkg/b.py"); rate != 0.75 {
		t.Fatalf("merged rate: %v", rate)
	}
}

func TestMergeKeepsHighestHit(t *testing.T) {
	a, _ := Parse([]byte(lcov))
	b, _ := Parse([]byte("SF:/work/src/lib/f.ts\nDA:2,4\nDA:9,0\nend_of_record\n"))
	a.Merge(b)
	m := a.Lookup("src/lib/f.ts")
	if m[2] != 4 || m[9] != 0 || m[5] != 2 {
		t.Fatalf("%v", m)
	}
	if _, err := Parse([]byte("  ")); err == nil {
		t.Fatal("empty input passed")
	}
	if _, err := Parse([]byte("hola")); err == nil {
		t.Fatal("arbitrary text passed")
	}
}
