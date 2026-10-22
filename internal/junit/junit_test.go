package junit

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// Real pytest 8 output with -o junit_family=xunit1 (trimmed): a pass, an assert, a NotImplementedError and a
// collection error.
const pytestXunit1 = `<?xml version="1.0" encoding="utf-8"?><testsuites><testsuite name="pytest" errors="1" failures="2" skipped="1" tests="5">
<testcase classname="tests.test_calc" name="test_suma" file="tests/test_calc.py" line="3" time="0.001" />
<testcase classname="tests.test_calc" name="test_redondea" file="tests/test_calc.py" line="7" time="0.001"><failure message="AssertionError: assert 3 == 2&#10; +  where 3 = redondea(2.4)">tests/test_calc.py:8: in test_redondea
    assert redondea(2.4) == 2
E   AssertionError</failure></testcase>
<testcase classname="tests.test_media" name="test_media" file="tests/test_media.py" line="3" time="0.001"><failure message="NotImplementedError: a2dlc stub">src/calc.py:20: NotImplementedError</failure></testcase>
<testcase classname="" name="tests.test_x" time="0.000"><error message="collection failure">ImportError while importing test module</error></testcase>
<testcase classname="tests.test_calc" name="test_lento" file="tests/test_calc.py" line="11" time="0.000"><skipped type="pytest.skip" message="lento" /></testcase>
</testsuite></testsuites>`

func TestParsePytest(t *testing.T) {
	cases, err := Parse([]byte(pytestXunit1))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"tests/test_calc.py::tests.test_calc::test_suma":     {contract.TestPassed, ""},
		"tests/test_calc.py::tests.test_calc::test_redondea": {contract.TestFailed, "AssertionError: assert 3 == 2\n +  where 3 = redondea(2.4)"},
		"tests/test_media.py::tests.test_media::test_media":  {contract.TestFailed, "NotImplementedError: a2dlc stub"},
		"junit::tests.test_x":                                {contract.TestError, "collection failure"},
		"tests/test_calc.py::tests.test_calc::test_lento":    {contract.TestSkipped, ""},
	}
	if len(cases) != len(want) {
		t.Fatalf("%d cases: %v", len(cases), cases)
	}
	for id, q := range want {
		c, ok := cases[id]
		if !ok || c.Status != q[0] || c.Message != q[1] {
			t.Errorf("%s = %+v, quiero %v", id, c, q)
		}
	}
}

// vitest puts the file as classname and has no file attribute; jest nests testsuites. The failure's type goes before
// the message if the message does not carry it.
func TestParseWithoutFileAndWithType(t *testing.T) {
	cases, err := Parse([]byte(`<testsuites><testsuites><testsuite name="s"><testcase classname="src/a.test.ts" name="suma"><failure type="TypeError" message="x is not a function"/></testcase></testsuite></testsuites></testsuites>`))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cases["src/a.test.ts::suma"]
	if !ok || c.Status != contract.TestFailed || c.Message != "TypeError: x is not a function" {
		t.Fatalf("%+v", cases)
	}
}

func TestParseRejectsNonJUnit(t *testing.T) {
	if _, err := Parse([]byte(`<html><body>no</body></html>`)); err == nil {
		t.Fatal("accepted something that is not JUnit")
	}
	if _, err := Parse([]byte(`<testsuites><testcase`)); err == nil {
		t.Fatal("accepted truncated XML")
	}
}

// A repeated id (a re-run inside the runner) keeps the worst status.
func TestParseDuplicateIDKeepsWorst(t *testing.T) {
	cases, _ := Parse([]byte(`<testsuite><testcase classname="a" name="t"/><testcase classname="a" name="t"><failure message="m"/></testcase></testsuite>`))
	if cases["a::t"].Status != contract.TestFailed {
		t.Fatalf("%+v", cases)
	}
}
