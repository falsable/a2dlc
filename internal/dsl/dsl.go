// Package dsl translates the invariant DSL of protocol §6.2 into SMT-LIB 2, the form Stage 1.5 sends to Z3 next to the
// raw smt clauses of S*. §6.2 names the subset (linear arithmetic over scaled integers or decimals, booleans, comparisons,
// sums over bounded collections) and not its concrete syntax; the syntax here is the core's and is documented by the
// examples in dsl_test.go:
//
//   - identifiers: letters, digits and underscores, with dotted segments (balance.after); xs[3] and xs[03] index a name
//     and both name the same symbol; xs[i] indexes by the variable of a sum
//   - integer literals, true and false. Decimals are not translated: a decimal is reported as runtime_and_pbt, and the
//     contract writes it scaled to integers (cents). §6.2 allows "scaled integers/decimals"; only the first half is
//     here. This package lives in internal/dsl, not internal/contract (decision 6 named the latter); the README records
//     both in its Deviations table.
//   - operators by precedence, lowest first: =>, ||, &&, comparisons (== != < <= > >=, not chained), + -, * (one
//     side constant), unary ! and -
//   - sum(i in LO..HI : expr) with integer bounds of at most MaxSumTerms iterations. All the sums of one invariant
//     together unroll to at most MaxSumUnrolled iterations, nested sums included.
//
// Outside the subset (quantifiers, temporal operators, function application, division, modulo, non-linear products,
// decimals, syntax errors and type mismatches) Translate returns an *Unsupported error with the reason. The caller marks
// the invariant verification: runtime_and_pbt and does not fail the stage (§6.2, §5 Stage 1.5 step 2).
package dsl

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MaxSumTerms bounds one sum's range: a sum over more iterations is outside the subset.
const MaxSumTerms = 64

// MaxSumUnrolled bounds the iterations of all the sums of one invariant, nested sums included: a sum of 64 that holds a
// sum of 64 unrolls to 4160 iterations, which is outside the subset even though each range is inside MaxSumTerms.
const MaxSumUnrolled = 256

// sumCap saturates the iteration count so that deep nesting cannot overflow; it is far above MaxSumUnrolled.
const sumCap = 1 << 40

// Unsupported is the error of an invariant outside the translatable subset; Reason says why, in words for the lint.
type Unsupported struct {
	Reason string
}

func (u *Unsupported) Error() string { return u.Reason }

func unsupported(format string, args ...any) error {
	return &Unsupported{Reason: fmt.Sprintf(format, args...)}
}

// Result is the SMT-LIB of one invariant: the declare-const of its symbols, sorted by name, and the assert.
type Result struct {
	Declarations []string
	Assertion    string
}

// Fragments are the lines the z3 engine concatenates for the invariant (its smt list).
func (r Result) Fragments() []string {
	return append(append([]string{}, r.Declarations...), r.Assertion)
}

// Translate turns one invariant's DSL expression into SMT-LIB. It returns *Unsupported for anything outside the subset.
func Translate(src string) (Result, error) {
	toks, err := lex(src)
	if err != nil {
		return Result{}, err
	}
	if len(toks) == 1 {
		return Result{}, unsupported("empty expression")
	}
	p := &parser{toks: toks}
	root, err := p.parseAll()
	if err != nil {
		return Result{}, err
	}
	// the whole expression is counted before anything is unrolled, so that no invariant can make Translate expand a lot
	if n := unrolled(root); n > MaxSumUnrolled {
		return Result{}, unsupported("the sums unroll to %d terms in all: the subset allows at most %d", n, MaxSumUnrolled)
	}
	expanded, err := expand(root, map[string]int64{})
	if err != nil {
		return Result{}, err
	}
	c := &checker{env: map[string]typ{}}
	if err := c.check(expanded, tBool); err != nil {
		return Result{}, err
	}
	if err := c.settle(); err != nil {
		return Result{}, err
	}
	names := make([]string, 0, len(c.env))
	for n := range c.env {
		names = append(names, n)
	}
	sort.Strings(names)
	res := Result{Declarations: []string{}}
	for _, n := range names {
		sortName := "Int"
		if c.env[n] == tBool {
			sortName = "Bool"
		}
		res.Declarations = append(res.Declarations, fmt.Sprintf("(declare-const |%s| %s)", n, sortName))
	}
	res.Assertion = "(assert " + emit(expanded) + ")"
	return res, nil
}

// ---- lexer

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tokInt
	tDecimal
	tOp
)

type token struct {
	kind tokKind
	text string
	pos  int
}

// operators, longest first so that "<=" wins over "<".
var operators = []string{"=>", "&&", "||", "==", "!=", "<=", ">=", "..", "<", ">", "+", "-", "*", "/", "%", "!", "(", ")", "[", "]", ":"}

// outside names the words that belong to a logic the subset does not translate, with the reason the lint reports.
var outside = map[string]string{
	"forall":     "unbounded quantifier forall",
	"exists":     "unbounded quantifier exists",
	"always":     "temporal operator always",
	"eventually": "temporal operator eventually",
	"never":      "temporal operator never",
	"until":      "temporal operator until",
	"next":       "temporal operator next",
	"old":        "old(...) refers to the pre-state",
}

// callOnly are the outside words that are operators only when called: "old" and "next" name an integer or a boolean
// when they are not followed by "(" (old + next == 2 is a plain invariant over two symbols).
var callOnly = map[string]bool{"old": true, "next": true}

func isLetter(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isDigit(c):
			start := i
			for i < len(src) && isDigit(src[i]) {
				i++
			}
			if i+1 < len(src) && src[i] == '.' && isDigit(src[i+1]) {
				i++
				for i < len(src) && isDigit(src[i]) {
					i++
				}
				toks = append(toks, token{tDecimal, src[start:i], start})
			} else {
				toks = append(toks, token{tokInt, src[start:i], start})
			}
		case isLetter(c):
			start := i
			for i < len(src) {
				if isLetter(src[i]) || isDigit(src[i]) {
					i++
				} else if src[i] == '.' && i+1 < len(src) && isLetter(src[i+1]) {
					i++
				} else {
					break
				}
			}
			toks = append(toks, token{tIdent, src[start:i], start})
		default:
			op := ""
			for _, o := range operators {
				if strings.HasPrefix(src[i:], o) {
					op = o
					break
				}
			}
			if op == "" {
				return nil, unsupported("character %q at %d is outside the subset", c, i)
			}
			toks = append(toks, token{tOp, op, i})
			i += len(op)
		}
	}
	return append(toks, token{kind: tEOF, pos: len(src)}), nil
}

// ---- parser

// expr is the syntax tree. op is "int", "bool", "id", "sum" or the operator as written; args are its operands.
type expr struct {
	op    string
	name  string // id: the name; sum: the index variable
	index string // id: what is written in [] ("" if none): digits or a sum variable
	val   int64  // int: the value; sum: the lower bound
	hi    int64  // sum: the upper bound
	args  []*expr
	pos   int
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

// callFollows: the token after the current one is "(", so the current word is a function application.
func (p *parser) callFollows() bool {
	next := p.toks[p.i+1]
	return next.kind == tOp && next.text == "("
}

func (p *parser) expectOp(s string) error {
	if !p.isOp(s) {
		return p.syntax("expected " + strconv.Quote(s))
	}
	p.i++
	return nil
}

func (p *parser) syntax(msg string) error {
	t := p.peek()
	where := "end of expression"
	if t.kind != tEOF {
		where = fmt.Sprintf("%q at %d", t.text, t.pos)
	}
	return unsupported("syntax: %s, found %s", msg, where)
}

func (p *parser) parseAll() (*expr, error) {
	e, err := p.parseImplies()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, p.syntax("expected the end of the expression")
	}
	return e, nil
}

func bin(op string, l, r *expr) *expr { return &expr{op: op, args: []*expr{l, r}, pos: l.pos} }
func un(op string, a *expr) *expr     { return &expr{op: op, args: []*expr{a}, pos: a.pos} }

func (p *parser) parseImplies() (*expr, error) {
	l, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.isOp("=>") {
		p.i++
		r, err := p.parseImplies()
		if err != nil {
			return nil, err
		}
		return bin("=>", l, r), nil
	}
	return l, nil
}

func (p *parser) parseOr() (*expr, error) {
	l, err := p.parseAnd()
	for err == nil && p.isOp("||") {
		p.i++
		var r *expr
		if r, err = p.parseAnd(); err == nil {
			l = bin("||", l, r)
		}
	}
	return l, err
}

func (p *parser) parseAnd() (*expr, error) {
	l, err := p.parseCmp()
	for err == nil && p.isOp("&&") {
		p.i++
		var r *expr
		if r, err = p.parseCmp(); err == nil {
			l = bin("&&", l, r)
		}
	}
	return l, err
}

var comparisons = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

func (p *parser) parseCmp() (*expr, error) {
	l, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == tOp && comparisons[t.text] {
		p.i++
		r, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		return bin(t.text, l, r), nil
	}
	return l, nil
}

func (p *parser) parseAdd() (*expr, error) {
	l, err := p.parseMul()
	for err == nil && (p.isOp("+") || p.isOp("-")) {
		op := p.peek().text
		p.i++
		var r *expr
		if r, err = p.parseMul(); err == nil {
			l = bin(op, l, r)
		}
	}
	return l, err
}

func (p *parser) parseMul() (*expr, error) {
	l, err := p.parseUnary()
	for err == nil && (p.isOp("*") || p.isOp("/") || p.isOp("%")) {
		op := p.peek().text
		if op != "*" {
			return nil, unsupported("division and modulo are outside the subset")
		}
		p.i++
		var r *expr
		if r, err = p.parseUnary(); err == nil {
			l = bin("*", l, r)
		}
	}
	return l, err
}

func (p *parser) parseUnary() (*expr, error) {
	if p.isOp("!") || p.isOp("-") {
		op := p.peek().text
		p.i++
		a, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return un(op, a), nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (*expr, error) {
	t := p.peek()
	switch {
	case t.kind == tokInt:
		v, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, unsupported("integer %s does not fit in 64 bits", t.text)
		}
		p.i++
		return &expr{op: "int", val: v, pos: t.pos}, nil
	case t.kind == tDecimal:
		return nil, unsupported("decimal literal %s is outside the subset: write it scaled to integers (cents)", t.text)
	case t.kind == tOp && t.text == "(":
		p.i++
		e, err := p.parseImplies()
		if err != nil {
			return nil, err
		}
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
		return e, nil
	case t.kind == tIdent:
		return p.parseWord(t)
	}
	return nil, p.syntax("expected a value")
}

func (p *parser) parseWord(t token) (*expr, error) {
	if reason, ok := outside[t.text]; ok && (!callOnly[t.text] || p.callFollows()) {
		return nil, unsupported("%s is outside the translatable subset", reason)
	}
	switch t.text {
	case "true", "false":
		p.i++
		return &expr{op: "bool", name: t.text, pos: t.pos}, nil
	case "sum":
		return p.parseSum()
	}
	p.i++
	if p.isOp("(") {
		return nil, unsupported("function application %s(...) is outside the subset", t.text)
	}
	e := &expr{op: "id", name: t.text, pos: t.pos}
	if p.isOp("[") {
		p.i++
		idx := p.peek()
		if idx.kind != tokInt && idx.kind != tIdent {
			return nil, p.syntax("expected an index")
		}
		p.i++
		if err := p.expectOp("]"); err != nil {
			return nil, err
		}
		e.index = idx.text
	}
	return e, nil
}

func (p *parser) parseSum() (*expr, error) {
	start := p.peek().pos
	p.i++ // sum
	if err := p.expectOp("("); err != nil {
		return nil, err
	}
	v := p.peek()
	if v.kind != tIdent || v.text == "in" {
		return nil, p.syntax("expected the index variable of the sum")
	}
	p.i++
	if in := p.peek(); in.kind != tIdent || in.text != "in" {
		return nil, p.syntax("expected 'in' after the index variable")
	}
	p.i++
	lo := p.peek()
	if lo.kind != tokInt {
		return nil, unsupported("sum bounds must be integer literals (sum over a bounded range)")
	}
	p.i++
	if err := p.expectOp(".."); err != nil {
		return nil, err
	}
	hi := p.peek()
	if hi.kind != tokInt {
		return nil, unsupported("sum bounds must be integer literals (sum over a bounded range)")
	}
	p.i++
	if err := p.expectOp(":"); err != nil {
		return nil, err
	}
	body, err := p.parseImplies()
	if err != nil {
		return nil, err
	}
	if err := p.expectOp(")"); err != nil {
		return nil, err
	}
	l, err1 := strconv.ParseInt(lo.text, 10, 64)
	h, err2 := strconv.ParseInt(hi.text, 10, 64)
	if err1 != nil || err2 != nil || l > h {
		return nil, unsupported("sum over %s..%s is empty or its bounds do not fit", lo.text, hi.text)
	}
	// h-l is compared, not h-l+1: the widest bounds would overflow the +1
	if h-l >= MaxSumTerms {
		if h-l == math.MaxInt64 {
			return nil, unsupported("sum over %s..%s is far beyond the subset, which unrolls at most %d terms", lo.text, hi.text, MaxSumTerms)
		}
		return nil, unsupported("sum over %d terms: the subset unrolls at most %d", h-l+1, MaxSumTerms)
	}
	return &expr{op: "sum", name: v.text, val: l, hi: h, args: []*expr{body}, pos: start}, nil
}

// unrolled counts the iterations of all the sums of e: a sum of n iterations counts n, and each of them counts what its body
// unrolls to. It saturates at sumCap, which no expression inside the subset reaches by accident.
func unrolled(e *expr) int64 {
	if e.op == "sum" {
		n := e.hi - e.val + 1 // at most MaxSumTerms: parseSum checked the range
		t := n * (1 + unrolled(e.args[0]))
		if t > sumCap {
			return sumCap
		}
		return t
	}
	var total int64
	for _, a := range e.args {
		total += unrolled(a)
		if total > sumCap {
			return sumCap
		}
	}
	return total
}

// ---- expansion: sums are unrolled and the indexes they bind become literals

func expand(e *expr, env map[string]int64) (*expr, error) {
	switch e.op {
	case "int", "bool":
		return e, nil
	case "id":
		if v, ok := env[e.name]; ok && e.index == "" {
			return &expr{op: "int", val: v, pos: e.pos}, nil
		}
		name := e.name
		if e.index != "" {
			idx := e.index
			if v, ok := env[e.index]; ok {
				idx = strconv.FormatInt(v, 10)
			} else if isDigits(e.index) {
				// xs[03] and xs[3] name the same element
				n, err := strconv.ParseInt(e.index, 10, 64)
				if err != nil {
					return nil, unsupported("index %s of %s does not fit in 64 bits", e.index, e.name)
				}
				idx = strconv.FormatInt(n, 10)
			} else {
				return nil, unsupported("index %s of %s is not bound by a sum", e.index, e.name)
			}
			name += "[" + idx + "]"
		}
		return &expr{op: "id", name: name, pos: e.pos}, nil
	case "sum":
		var terms []*expr
		for k := e.val; k <= e.hi; k++ {
			inner := map[string]int64{}
			for n, v := range env {
				inner[n] = v
			}
			inner[e.name] = k
			t, err := expand(e.args[0], inner)
			if err != nil {
				return nil, err
			}
			terms = append(terms, t)
		}
		sum := terms[0]
		for _, t := range terms[1:] {
			sum = bin("+", sum, t)
		}
		return sum, nil
	}
	out := &expr{op: e.op, pos: e.pos}
	for _, a := range e.args {
		x, err := expand(a, env)
		if err != nil {
			return nil, err
		}
		out.args = append(out.args, x)
	}
	return out, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// ---- types and checks

type typ int

const (
	tUnknown typ = iota
	tInt
	tBool
)

// typeOf is the type an expression has on its own, when it has one; identifiers take the type the environment knows.
func typeOf(e *expr, env map[string]typ) typ {
	switch e.op {
	case "int", "+", "*":
		return tInt
	case "-":
		return tInt
	case "bool", "!", "&&", "||", "=>", "==", "!=", "<", "<=", ">", ">=":
		return tBool
	case "id":
		return env[e.name]
	}
	return tUnknown
}

// sortOfSides is the type of an equality's operands: the first side that has one, tUnknown if neither does.
func sortOfSides(e *expr, env map[string]typ) typ {
	if t := typeOf(e.args[0], env); t != tUnknown {
		return t
	}
	return typeOf(e.args[1], env)
}

func typeName(t typ) string {
	if t == tBool {
		return "a boolean"
	}
	return "an integer"
}

// checker assigns the sorts of the identifiers and rejects what the subset does not allow.
type checker struct {
	env map[string]typ
	// deferred are the equalities whose two sides are bare identifiers of no known sort: "p == q && p" says p is boolean
	// only after the equality was seen, so the equality waits for the rest of the expression (settle).
	deferred []*expr
}

// check walks e expecting the type want (tUnknown when the context does not care).
func (c *checker) check(e *expr, want typ) error {
	switch e.op {
	case "int", "bool":
		got := typeOf(e, c.env)
		if want != tUnknown && got != want {
			return unsupported("%s where %s is expected", typeName(got), typeName(want))
		}
		return nil
	case "id":
		if want == tUnknown {
			return nil
		}
		if got := c.env[e.name]; got == tUnknown {
			c.env[e.name] = want
		} else if got != want {
			return unsupported("%s is used as %s and as %s", e.name, typeName(got), typeName(want))
		}
		return nil
	case "+", "-":
		if err := expectType(want, tInt); err != nil {
			return err
		}
		return c.each(e.args, tInt)
	case "*":
		if err := expectType(want, tInt); err != nil {
			return err
		}
		if !isConst(e.args[0]) && !isConst(e.args[1]) {
			return unsupported("product of two non-constant terms is not linear")
		}
		return c.each(e.args, tInt)
	case "!", "&&", "||", "=>":
		if err := expectType(want, tBool); err != nil {
			return err
		}
		return c.each(e.args, tBool)
	case "<", "<=", ">", ">=":
		if err := expectType(want, tBool); err != nil {
			return err
		}
		return c.each(e.args, tInt)
	case "==", "!=":
		if err := expectType(want, tBool); err != nil {
			return err
		}
		t := sortOfSides(e, c.env)
		if t == tUnknown {
			c.deferred = append(c.deferred, e)
			return nil
		}
		return c.each(e.args, t)
	}
	return unsupported("internal: operator %s", e.op)
}

func (c *checker) each(args []*expr, want typ) error {
	for _, a := range args {
		if err := c.check(a, want); err != nil {
			return err
		}
	}
	return nil
}

// settle types the deferred equalities once the rest of the expression has typed what it can. An equality that nothing
// else types is between integers, as x != y is in the golden tests; it does not stay unknown.
func (c *checker) settle() error {
	for len(c.deferred) > 0 {
		pending := c.deferred
		c.deferred = nil
		resolved := false
		for _, e := range pending {
			t := sortOfSides(e, c.env)
			if t == tUnknown {
				c.deferred = append(c.deferred, e)
				continue
			}
			resolved = true
			if err := c.each(e.args, t); err != nil {
				return err
			}
		}
		if !resolved && len(c.deferred) > 0 {
			e := c.deferred[0]
			c.deferred = c.deferred[1:]
			if err := c.each(e.args, tInt); err != nil {
				return err
			}
		}
	}
	return nil
}

func expectType(want, got typ) error {
	if want != tUnknown && want != got {
		return unsupported("%s where %s is expected", typeName(got), typeName(want))
	}
	return nil
}

// isConst: the expression is a constant, built from integer literals only.
func isConst(e *expr) bool {
	switch e.op {
	case "int":
		return true
	case "+", "-", "*":
		for _, a := range e.args {
			if !isConst(a) {
				return false
			}
		}
		return true
	}
	return false
}

// ---- emission

func emit(e *expr) string {
	switch e.op {
	case "int":
		return strconv.FormatInt(e.val, 10)
	case "bool":
		return e.name
	case "id":
		return "|" + e.name + "|"
	case "!":
		return "(not " + emit(e.args[0]) + ")"
	case "-":
		if len(e.args) == 1 {
			return "(- " + emit(e.args[0]) + ")"
		}
	case "!=":
		return "(not (= " + emit(e.args[0]) + " " + emit(e.args[1]) + "))"
	case "==":
		return "(= " + emit(e.args[0]) + " " + emit(e.args[1]) + ")"
	}
	smt := map[string]string{"+": "+", "-": "-", "*": "*", "&&": "and", "||": "or", "=>": "=>",
		"<": "<", "<=": "<=", ">": ">", ">=": ">="}[e.op]
	return "(" + smt + " " + emit(e.args[0]) + " " + emit(e.args[1]) + ")"
}
