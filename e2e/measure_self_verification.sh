#!/usr/bin/env bash
# The ADLC rule "every session verifies its own work first", made executable, over the working trees that
# e2e/measure_gates.sh built (one folder per frozen gates-py case, candidate overlay on the base). Two readings:
#   changed: run only the test files the session changed; green, or nothing to run, means done.
#   suite:   run the whole suite; green means done. The base ships tests/test_roto.py, red before any change, so
#            this reading also reports whether the only red tests are the ones already red on the base (the
#            bookkeeping a2dlc's baseline gate automates).
# It needs a2dlc-tests-pytest:0.2. Usage: bash e2e/measure_self_verification.sh <folder given to measure_gates.sh>
set -u
G=$1
[ -d "$G" ] || { echo "no such folder: $G"; exit 1; }
run_pytest() { # <tree> <args...>
	local W=$1
	shift
	MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$W" 2>/dev/null || echo "$W"):/work" -w /work \
		a2dlc-tests-pytest:0.2 python -m pytest -q -p no:cacheprovider -rf "$@" 2>&1
}
for W in "$G"/C*/; do
	id=$(basename "$W")
	cd "$W" || exit 1
	changed=$(git status --porcelain -- tests | awk '{print $2}' | tr '\n' ' ')
	if [ -z "$changed" ]; then
		c="GREEN(nothing to run)"
	elif run_pytest "$W" $changed >"$W.changed.out"; then
		c="GREEN"
	else
		c="RED"
	fi
	run_pytest "$W" tests >"$W.suite.out"
	if [ $? -eq 0 ]; then
		s="GREEN"
	else
		red=$(grep -E "^FAILED|^ERROR" "$W.suite.out" | sed 's/ - .*//' | cut -d' ' -f2 | sort -u | tr '\n' ' ')
		if [ -n "$red" ] && [ "$(echo "$red" | tr ' ' '\n' | grep -v '^$' | grep -vc 'test_roto.py')" -eq 0 ]; then
			s="RED(only the base's red: $red)"
		else
			s="RED($red)"
		fi
	fi
	echo "$id changed=$c suite=$s"
done
