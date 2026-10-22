package dsl

import (
	"errors"
	"strings"
	"testing"
)

// golden: the SMT-LIB fragments of each invariant the subset translates, as Stage 1.5 sends them to Z3.
var golden = []struct {
	name, src string
	want      []string
}{
	{"bound", "balance >= 0", []string{
		"(declare-const |balance| Int)",
		"(assert (>= |balance| 0))"}},
	{"range", "amount > 0 && amount <= 1000", []string{
		"(declare-const |amount| Int)",
		"(assert (and (> |amount| 0) (<= |amount| 1000)))"}},
	{"conservation", "debit + credit == total", []string{
		"(declare-const |credit| Int)",
		"(declare-const |debit| Int)",
		"(declare-const |total| Int)",
		"(assert (= (+ |debit| |credit|) |total|))"}},
	{"boolean implication", "!frozen || balance >= 0", []string{
		"(declare-const |balance| Int)",
		"(declare-const |frozen| Bool)",
		"(assert (or (not |frozen|) (>= |balance| 0)))"}},
	{"boolean equal to a comparison", "ok == (x > 0)", []string{
		"(declare-const |ok| Bool)",
		"(declare-const |x| Int)",
		"(assert (= |ok| (> |x| 0)))"}},
	{"linear with a constant factor", "2 * x - y <= 10", []string{
		"(declare-const |x| Int)",
		"(declare-const |y| Int)",
		"(assert (<= (- (* 2 |x|) |y|) 10))"}},
	{"bounded sum over indexes", "sum(i in 0..2 : xs[i]) == total", []string{
		"(declare-const |total| Int)",
		"(declare-const |xs[0]| Int)",
		"(declare-const |xs[1]| Int)",
		"(declare-const |xs[2]| Int)",
		"(assert (= (+ (+ |xs[0]| |xs[1]|) |xs[2]|) |total|))"}},
	{"inequality", "x != y", []string{
		"(declare-const |x| Int)",
		"(declare-const |y| Int)",
		"(assert (not (= |x| |y|)))"}},
	{"unary minus", "-x < 0", []string{
		"(declare-const |x| Int)",
		"(assert (< (- |x|) 0))"}},
	{"implication between booleans", "ready => paid", []string{
		"(declare-const |paid| Bool)",
		"(declare-const |ready| Bool)",
		"(assert (=> |ready| |paid|))"}},
	{"dotted names", "balance.after == balance.before - amount", []string{
		"(declare-const |amount| Int)",
		"(declare-const |balance.after| Int)",
		"(declare-const |balance.before| Int)",
		"(assert (= |balance.after| (- |balance.before| |amount|)))"}},
	// two boolean sides of an equality that no other operator types: the && types p as boolean after the equality is seen
	{"equal booleans", "p == q && p", []string{
		"(declare-const |p| Bool)",
		"(declare-const |q| Bool)",
		"(assert (and (= |p| |q|) |p|))"}},
	// xs[03] and xs[3] are the same element: one symbol
	{"index with a leading zero", "xs[03] == xs[3]", []string{
		"(declare-const |xs[3]| Int)",
		"(assert (= |xs[3]| |xs[3]|))"}},
	// old and next are operators only when called: as names they are plain integers
	{"old and next as names", "old + next == 2", []string{
		"(declare-const |next| Int)",
		"(declare-const |old| Int)",
		"(assert (= (+ |old| |next|) 2))"}},
	// compiled-policies business invariants: conservation, budget ceiling and boolean implications
	{"policy budget non-negative", "presupuesto_final >= 0", []string{
		"(declare-const |presupuesto_final| Int)",
		"(assert (>= |presupuesto_final| 0))"}},
	{"policy budget ceiling", "presupuesto_final <= presupuesto_inicial", []string{
		"(declare-const |presupuesto_final| Int)",
		"(declare-const |presupuesto_inicial| Int)",
		"(assert (<= |presupuesto_final| |presupuesto_inicial|))"}},
	{"policy conservation", "presupuesto_final == presupuesto_inicial - total_aprobado", []string{
		"(declare-const |presupuesto_final| Int)",
		"(declare-const |presupuesto_inicial| Int)",
		"(declare-const |total_aprobado| Int)",
		"(assert (= |presupuesto_final| (- |presupuesto_inicial| |total_aprobado|)))"}},
	{"policy cancellation boolean implication", "cancelada => !revision_it", []string{
		"(declare-const |cancelada| Bool)",
		"(declare-const |revision_it| Bool)",
		"(assert (=> |cancelada| (not |revision_it|)))"}},
}

// unsat: a contradiction is a valid translation; Z3 answers unsat for it (CB-02a) and the Go test does not run the solver.
var unsat = struct {
	src  string
	want []string
}{
	"x > 0 && x < 0",
	[]string{"(declare-const |x| Int)", "(assert (and (> |x| 0) (< |x| 0)))"},
}

// unsupported: each one is outside the subset with the reason the lint shows; none of them is a syntax of the core's error.
var unsupported_ = []struct {
	name, src, reason string
}{
	{"quantifier", "forall i in xs : i > 0", "unbounded quantifier forall"},
	{"temporal", "always balance >= 0", "temporal operator always"},
	{"function application", "f(f(x)) == f(x)", "function application f(...)"},
	{"non-linear product", "x * y > 0", "product of two non-constant terms is not linear"},
	{"division", "x / 2 == 1", "division and modulo are outside the subset"},
	{"decimal", "price == 1.5", "decimal literal 1.5 is outside the subset"},
	{"sum too long", "sum(i in 0..100 : xs[i]) == 0", "sum over 101 terms"},
	{"integer where boolean", "x + 1", "an integer where a boolean is expected"},
	{"boolean and integer", "x == 1 && x", "x is used as an integer and as a boolean"},
	{"unbound index", "sum(i in 0..2 : xs[k]) > 0", "index k of xs is not bound by a sum"},
	{"chained comparison", "x == y == z", "syntax: expected the end of the expression"},
	{"dangling operator", "a > 0 &&", "syntax: expected a value"},
	{"empty", "   ", "empty expression"},
	{"foreign character", "x @ 2", "character '@' at 2 is outside the subset"},
	{"old called", "old(x) > 0", "old(...) refers to the pre-state"},
	// each sum is inside MaxSumTerms; the nest unrolls to 64 + 64*64 iterations, past the 256 of the whole invariant
	{"nested sums", "sum(i in 0..63 : sum(j in 0..63 : xs[j])) == 0", "the sums unroll to 4160 terms in all"},
	{"five sums of 64", "sum(i in 0..63 : xs[i]) + sum(j in 0..63 : xs[j]) + sum(k in 0..63 : xs[k]) + sum(l in 0..63 : xs[l]) + sum(m in 0..63 : xs[m]) == 0", "the sums unroll to 320 terms in all"},
	{"bounds beyond the int64 count", "sum(i in 0..9223372036854775807 : xs[i]) == 0", "is far beyond the subset"},
}

func TestGoldenTranslations(t *testing.T) {
	for _, g := range golden {
		r, err := Translate(g.src)
		if err != nil {
			t.Errorf("%s: %v", g.name, err)
			continue
		}
		if got := strings.Join(r.Fragments(), "\n"); got != strings.Join(g.want, "\n") {
			t.Errorf("%s:\n got %s\nwant %s", g.name, got, strings.Join(g.want, "\n"))
		}
	}
}

func TestUnsatTranslationIsWellFormed(t *testing.T) {
	r, err := Translate(unsat.src)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Fragments(), "\n"); got != strings.Join(unsat.want, "\n") {
		t.Errorf("got %s", got)
	}
}

func TestUnsupportedIsNotASyntaxFailureOfTheStage(t *testing.T) {
	for _, c := range unsupported_ {
		_, err := Translate(c.src)
		var u *Unsupported
		if !errors.As(err, &u) {
			t.Errorf("%s: want *Unsupported, got %v", c.name, err)
			continue
		}
		if !strings.Contains(u.Reason, c.reason) {
			t.Errorf("%s: reason %q does not say %q", c.name, u.Reason, c.reason)
		}
	}
}

// sums are bounded in all, not one by one: nested sums multiply, and sibling sums add up
func TestSumsAreBoundedInAll(t *testing.T) {
	for _, src := range []string{
		"sum(i in 0..63 : xs[i]) + sum(j in 0..63 : ys[j]) == 0",
		"sum(i in 0..3 : sum(j in 0..3 : xs[j])) == 0",
	} {
		if _, err := Translate(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestSumLimitIsTheSubsetBound(t *testing.T) {
	// exactly MaxSumTerms terms still translate; one more is outside the subset
	if _, err := Translate("sum(i in 0..63 : xs[i]) == 0"); err != nil {
		t.Fatalf("64 terms: %v", err)
	}
	if _, err := Translate("sum(i in 0..64 : xs[i]) == 0"); err == nil {
		t.Fatal("65 terms translated")
	}
}
