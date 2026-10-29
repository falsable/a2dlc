# Coverage notes: unexamined lines that do not block

Bead a2dlc-os3. Owner decision of 2026-10-26 (option A).

## Decision

The coverage precondition (VARA-2 rule 1) says every changed line in scope is examined by an engine target, executed by a
test, or the verdict is `INCONCLUSIVE`. On five real agencia-ia commits that rule made five of five `INCONCLUSIVE`, and
the lines it complained about were mostly `except Exception:` handlers and modules that only declare. A gate that always
says `INCONCLUSIVE` gets skipped.

Two categories of unexamined line now go to the receipt as a **note** and no longer make the verdict `INCONCLUSIVE` on
their own. Every other unexamined line blocks as before. The risk the owner accepts: a handler that swallows a real error
passes without a test. The receipt keeps the lines, so the gap is stated, not hidden.

## Where the classification lives

In the engine, not the core. What counts as a handler or as a declaration depends on the language, and the core knows no
languages. The core asks the engine that owns each file (`operation: classify_lines`, `budget.lines` = file → unexamined
lines) and trusts the answer only as far as it lowers what blocks:

- the response must name requested files only, with lines that were asked about, each in at most one note, and a category
  from the closed list below; a response that breaks any of this is dropped whole;
- a runner without the capability, a file no engine owns, an engine that predates the operation, an error or a timeout
  leave every line unexamined, which is the behavior before this change. A language without a classifier behaves exactly
  as before.

The Python engine (`engines/py/classify.py`) and the TypeScript engine (`engines/ts/classify.ts`, bead a2dlc-0sk) classify
today. The categories are `defensive_handler` and `declarative_only`; adding a language adds a classifier to its engine,
not code to the core.

## Receipt

`coverage.files[].noted` (optional; absent in older receipts) lists `{category, lines}`. Noted lines are removed from
`uncovered`, so `uncovered` stays "what blocks":

```json
{"file": "app/api/routes/governance.py", "by_engine": [], "covered": [112, 113, 118], "uncovered": [],
 "noted": [{"category": "defensive_handler", "lines": [119, 120]}]}
```

`a2dlc check` prints a line with the count of noted lines.

## Python rules

Written to be narrow: a line is classified only when the whole construct is. Anything in doubt blocks.

**defensive_handler.** An `except` clause that catches broadly (bare, `Exception` or `BaseException`, alone or in a tuple)
and whose whole body is inert. Inert statements: `pass`, `...` or a docstring, `continue`, `break`, a bare `raise`,
assignment of an inert value to a name or attribute, `return` of an inert value, and a logging call (`logger.warning(...)`,
the receiver's name contains `log`) whose arguments are inert. The header and every body line are noted. Not classified: a
handler for a specific exception, a call that is not logging (`db.rollback()`, `build(e)`), `raise X(...)`, an `if`, a loop,
a nested `try`, `await`, subscripts.

**declarative_only.** Imports; `__all__ = [...]` of string literals; an `if TYPE_CHECKING:` block holding only imports; and a
class skeleton: a class whose body is only a docstring, `pass`/`...`, annotated or plain assignments of inert values, nested
skeletons and stub methods (body of only a docstring, `pass`, `...` or `raise NotImplementedError`), with decorators from a
short list (`runtime_checkable`, `abstractmethod`, `dataclass`, `property`, ...). A Protocol with one real method is not a
skeleton. Not classified: any other module-level assignment (`THRESHOLD = 0.5`, `logger = getLogger(...)`), a route
decorator, a function that is not a stub method of a skeleton.

An inert value is a literal, a name, an attribute chain, a unary minus or `not`, or a tuple, list, set, dict or f-string of
inert values; a call never is, except `str()`, `repr()` and `type()` inside the arguments of a logging call.

## TypeScript rules

Bead a2dlc-0sk. Node's V8 coverage over tsx source maps reports the lines the compiler erases (a type alias, an interface,
a type-only import) at zero hits, because no JavaScript was emitted for them. A changed type could never be "executed", so
the verdict stayed `INCONCLUSIVE` because of it (seen on the Entropos candidate: `type Protocol = 'openai' | 'anthropic' |
'mcp';`). Only `.ts`, `.tsx`, `.mts` and `.cts` are read, and a file that does not parse is not classified.

**declarative_only.** A line with no executable token on it. That is every line of a type alias; an interface; a type-only
import (`import type ...`, or a specifier list where every specifier is `type`); a type-only export (`export type { A }`,
`export type * from`, or all specifiers `type`); anything marked `declare` (`declare const`, `declare module`, `declare
global`, `export declare class`), also inside a function body. It is also every blank or comment-only line, because the
tool does not only zero the erased declaration: on the Entropos candidate (`f59c55f`, `src/server/budgetGuard.ts`) lcov
had `DA:76,16` for the alias and `DA:77,0` for the blank line after it, and that blank line was the one line left
unexamined. A line that shares a token with executable code is not noted (`type A = number; const a: A = 1;`), and neither
is a blank line inside a template literal. Not classified: value imports (they run at load time), `import './x'`, enums,
namespaces with values, abstract classes, overload signatures, `.d.ts` files as a whole, and defensive `catch` blocks (the
TypeScript engine has no `defensive_handler` yet). A changed executable line at zero hits stays unexamined.

Checked on that candidate with the engine image built from this change: before, `coverage.files[].uncovered` of
`budgetGuard.ts` was `[77]` ("149 changed lines examined and 1 not examined"); after, `uncovered` is `[]` and `noted` is
`[{"category":"declarative_only","lines":[77]}]` ("0 not examined"). The images that run in real gates must be rebuilt from
`engines/ts` to get this: an image built before this change answers `classify_lines` as an unknown operation, and the core
then keeps every line unexamined.

Tests: `engines/ts/tests/classify.test.mjs` (the real compiler), `internal/app/coverage_notes_test.go`
(`TestCheckNotesTypeOnlyLinesCoverageReportsAtZero`, the verdict side with lcov `DA:n,0`).

## The five commits

Fixtures in `engines/py/testdata/classify/` (minimal code of each commit, `cases.json`); the engine tests check the real
classifier against them and `internal/app/coverage_notes_test.go` checks the verdict side.

| Commit | Unexamined lines | Result |
| --- | --- | --- |
| 69fb47d | four `ports/*.py` modules: imports, `__all__`, `Protocol` classes | all noted, no coverage block |
| b7cc3c5 | `except Exception:` + `anclas = None`; `except` + `logger.warning` + `return {...}` | all noted, no coverage block |
| 60f5c6b | `try:`, `await AuditLedger().entries(tid)`, `except Exception:`, `no_evaluado = None` | handler noted; `try:` and the call still block |
| 141097e | `async with _db.session_factory()` and the DB call | blocks |
| 767a5cb | assignments in `if/elif/else` branches | blocks |

60f5c6b is the one that differs from the bead's expectation. The call in the `try` body is real code that no test ran,
and a call to the ledger is exactly the kind of line the owner said must not become a note. So the handler is noted and the
verdict stays `INCONCLUSIVE` for the `try:` line and the call. The alternative, treating a try body guarded by a defensive
handler as part of the handler, would hide every unexamined database call that happens to sit inside a `try`.
