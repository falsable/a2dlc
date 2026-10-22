#!/bin/sh
# tests.command of VARA-2. Runs inside the tests image derived from the SWE-bench one (tests.Dockerfile).
# The package is installed *editable* in /testbed (and matplotlib has compiled .so files that git ignores), so the
# tree exported by a2dlc (/work) is copied over /testbed and the tests run there. Runs ONLY the files in
# $A2DLC_TESTS, leaves JUnit in $A2DLC_JUNIT and Cobertura XML in $A2DLC_COVERAGE. No network.
set -u
P=/opt/miniconda3/envs/testbed/bin/python
cp -a /work/. /testbed/ || exit 1
cd /testbed || exit 1
rm -f .coverage .coverage.* tests/.coverage tests/.coverage.* 2>/dev/null
# The precondition only looks at the lines of the changed files ($A2DLC_CHANGED): measuring only those makes tracing
# cheap (coverage over all of sympy took longer than the tests) and leaves a small XML.
INC=""
for f in ${A2DLC_CHANGED:-}; do INC="$INC${INC:+,}/testbed/$f"; done
if [ -n "$INC" ]; then
	COV="-m coverage run --rcfile=/dev/null --include=$INC"
	XML() { $P -m coverage xml -q --rcfile=/dev/null -o "$A2DLC_COVERAGE" 2>/dev/null; }
else
	COV=""   # no changed sources means no lines to examine: no tracing
	XML() { :; }
fi
if [ -z "${A2DLC_TESTS:-}" ]; then
	printf '<testsuites></testsuites>\n' >"$A2DLC_JUNIT"
	exit 0
fi
if [ -f tests/runtests.py ] && [ -f tests/test_sqlite.py ]; then
	# django: its runner does not write JUnit; the log is converted, with verbosity 2. From tests/ so test_sqlite imports.
	MODULES=""
	for f in $A2DLC_TESTS; do
		m=${f#tests/}
		m=${m%.py}
		MODULES="$MODULES $(printf '%s' "$m" | tr / .)"
	done
	cd tests || exit 1
	$P $COV ./runtests.py --verbosity 2 --settings=test_sqlite --parallel 1 $MODULES >/tmp/run.log 2>&1
	rc=$?
	$P /a2dlc/log2junit_django.py /tmp/run.log >"$A2DLC_JUNIT"
	XML
	tail -5 /tmp/run.log
	exit $rc
fi
$P $COV -m pytest -p no:cacheprovider -o junit_family=xunit1 --junitxml="$A2DLC_JUNIT" -q $A2DLC_TESTS >/tmp/run.log 2>&1
rc=$?
XML
tail -5 /tmp/run.log
exit $rc
