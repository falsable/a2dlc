# Shared by the e2e/checks measurements. Expects R (repo), D (gates-py fixture), T (work folder), OUT (results folder),
# A2 (binary). tree <case> <label> <config>: a tree with the base committed with that config and the candidate overlay
# uncommitted, as measure_gates.sh does. check <label>: runs the gate, saves its outputs under $T/outputs and appends the
# checks to $TSV. validate: every saved verdict and escalation file against the schema.
tree() {
	local id=$1 label=$2 cfg=$3 W=$T/trees/$2
	rm -rf "$W"
	mkdir -p "$W/.a2dlc" && cp -r "$D/base/." "$W/" && cd "$W" || exit 1
	git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
	printf '%s' "$cfg" >.a2dlc/config.json
	printf '.a2dlc/verdict.json\n.a2dlc/escalations.json\n.a2dlc/reports/\n' >.gitignore
	git add -A && git commit -qm base
	cp -r "$D/casos/$id/." "$W/"
	rm -rf "$W/.claude" "$W/.a2dlc/waivers.json"
}
check() {
	local label=$1 W=$T/trees/$1 exit
	cd "$W" || exit 1
	"$A2" check >"$T/outputs/$label.log" 2>&1
	exit=$?
	cp .a2dlc/verdict.json "$T/outputs/verdict__$label.json"
	cp .a2dlc/escalations.json "$T/outputs/escalation__$label.json"
	python3 -I "$R/e2e/checks/tabulate.py" "$label" "$exit" .a2dlc/verdict.json >>"$TSV"
	echo "$label: exit $exit"
}
validate() {
	MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$R/contracts"):/c:ro" -v "$(cygpath -w "$T/outputs"):/s:ro" \
		a2dlc-contract-test python /c/tests/validate.py /c /s >"$T/conformance.txt"
	echo "schema conformance: exit $? ($(grep -c '^ok  output' "$T/conformance.txt") outputs validated, $(grep -c '^BAD' "$T/conformance.txt") failed)"
}
