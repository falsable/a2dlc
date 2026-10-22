// a2dlc TypeScript engine, operation mutate (Stage 5, Tier 2, protocol §5). It mirrors the Python mutate of
// engines/py/engine.py: the same operators and descriptions, the same pruning reasons and the response of
// contracts/engine-mutate-response.schema.json. Deviation: TypeScript has no floor division, so % maps to * where the
// Python contract lists % and //. The dispatch branch in input.mjs and the COPY in the Dockerfile are not in this
// package (routed to ts-a in needs): until they land, operation mutate is not reachable. engine.ts is not touched
// because its sha256 (engine-v2.sha256) pins the v2 generators measured.
//
// Positions: the TypeScript AST counts UTF-16 code units; the mutant files are spliced as UTF-8 bytes, so each edit is
// converted from UTF-16 offsets to byte offsets of the original text before it is applied.
import ts from 'typescript';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';

export type Operator = 'arithmetic' | 'comparison' | 'negate_condition' | 'constant' | 'return_none' | 'remove_call';

// Prose values of the prune reasons. They are the values of internal/contract/mutation.go (PruneLogging, PruneAssert,
// PruneDeadBranch, PruneDeadBranchTypes); the core validates them against the response schema.
export const REASON_LOG = 'equivalent: logging-only statement';
export const REASON_ASSERT = 'equivalent: assert';
export const REASON_BRANCH = 'equivalent: dead branch (constant condition)';
export const REASON_BRANCH_TYPES = 'equivalent: type-guaranteed dead branch';

// Calls whose statement is only logging (Python: logging., logger., log., print). console.assert is an assert, not logging.
const LOG_PREFIXES = ['console.', 'logging.', 'logger.', 'log.'];
const ARITHMETIC = new Map([['+', '-'], ['-', '+'], ['*', '/'], ['/', '*'], ['%', '*'], ['**', '*']]);
const ARITHMETIC_ASSIGN = new Map([['+=', '-='], ['-=', '+='], ['*=', '/='], ['/=', '*='], ['%=', '*='], ['**=', '*=']]);
const COMPARISON = new Map([['<', '<='], ['<=', '<'], ['>', '>='], ['>=', '>'], ['==', '!='], ['!=', '=='],
  ['===', '!=='], ['!==', '===']]);
const CONDITION_KIND = 'a conditional expression';

export interface Edit { start: number; end: number; text: string } // UTF-16 offsets in the original text
export interface Pruned { line: number; reason: string }
export interface Mutant { line: number; operator: Operator; description: string; bytes: Buffer }
export interface Analysis {
  mutants: Mutant[];
  pruned: Pruned[];
  uncompilable: { line: number; description: string }[];
}
export interface AnalysisOptions {
  // Parse check of a mutated text; defaults to parseErrors. A fake can be injected by the tests.
  parseErrorsOf?: (text: string) => string[];
}

interface Change { line: number; pos: number; operator: Operator; description: string; edits: Edit[] }
interface Span { start: number; end: number; reason: string }

const TS_FILE = /\.(ts|tsx|mts|cts)$/;

const scriptKind = (fileName: string): ts.ScriptKind => (fileName.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
const virtualName = (fileName: string): string => `/candidate/${fileName}`;

// Syntax errors of a text, as messages. The parser is tolerant: a file with errors still yields a tree, so the
// diagnostics are read from the source file (parseDiagnostics is internal to the compiler but stable since TS 2).
export function parseErrors(fileName: string, text: string): string[] {
  const sf = ts.createSourceFile(virtualName(fileName), text, ts.ScriptTarget.Latest, false, scriptKind(fileName));
  const diagnostics = (sf as unknown as { parseDiagnostics?: readonly ts.Diagnostic[] }).parseDiagnostics ?? [];
  return diagnostics.map((d) => ts.flattenDiagnosticMessageText(d.messageText, ' '));
}

// Nodes whose text is not code the mutants may change: type positions, type-level declarations and imports or exports
// (their string is a module specifier, not a value).
function skipped(n: ts.Node): boolean {
  return ts.isTypeNode(n) || ts.isTypeParameterDeclaration(n) || ts.isInterfaceDeclaration(n)
    || ts.isTypeAliasDeclaration(n) || ts.isImportDeclaration(n) || ts.isImportEqualsDeclaration(n)
    || ts.isExportDeclaration(n);
}

// require(...) and import(...): the callee that loads a module by its specifier.
function isModuleLoader(e: ts.Expression): boolean {
  return e.kind === ts.SyntaxKind.ImportKeyword || (ts.isIdentifier(e) && e.text === 'require');
}

// A string that is a statement on its own (a directive such as "use strict"), a module name, or the specifier of
// require() or import(): not a value. Mutating a specifier breaks module resolution, which any test would kill.
function isLooseString(n: ts.Node): boolean {
  const parent = n.parent;
  if (parent === undefined) return false;
  if (ts.isExpressionStatement(parent) || ts.isModuleDeclaration(parent)) return true;
  return ts.isCallExpression(parent) && parent.arguments[0] === n && isModuleLoader(parent.expression);
}

function dotted(e: ts.Expression): string | null {
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) {
    const left = dotted(e.expression);
    return left === null ? null : `${left}.${e.name.text}`;
  }
  return null;
}

// Truth value of a literal condition: true, false or 0 (Python: True, False, 0). null for anything else.
function constantOf(e: ts.Expression): boolean | null {
  if (e.kind === ts.SyntaxKind.TrueKeyword) return true;
  if (e.kind === ts.SyntaxKind.FalseKeyword) return false;
  if (ts.isNumericLiteral(e) && Number(e.text) === 0) return false;
  return null;
}

function conditionOf(n: ts.Node): [ts.Expression, string] | null {
  if (ts.isIfStatement(n)) return [n.expression, 'an if'];
  if (ts.isWhileStatement(n)) return [n.expression, 'a while'];
  if (ts.isDoStatement(n)) return [n.expression, 'a do-while'];
  if (ts.isConditionalExpression(n)) return [n.condition, CONDITION_KIND];
  return null;
}

// Applies the edits to the original bytes. The edits are in UTF-16 offsets of `text`; each one is converted to its
// UTF-8 byte offset in the original, and applied from the last to the first so that earlier offsets stay valid.
function splice(text: string, original: Buffer, edits: Edit[]): Buffer {
  let out = original;
  const byteOffset = (i: number): number => Buffer.byteLength(text.slice(0, i), 'utf8');
  for (const e of [...edits].sort((a, b) => b.start - a.start)) {
    const a = byteOffset(e.start);
    const b = byteOffset(e.end);
    out = Buffer.concat([out.subarray(0, a), Buffer.from(e.text, 'utf8'), out.subarray(b)]);
  }
  return out;
}

// The mutants of the requested lines of one file, and the equivalent mutants pruned from them. Pruning is conservative
// (what the AST or the checker proves without running): any other node is mutated as usual.
export function mutantsOf(text: string, fileName: string, lines: ReadonlySet<number>, opts: AnalysisOptions = {}): Analysis {
  const sf = ts.createSourceFile(virtualName(fileName), text, ts.ScriptTarget.Latest, true, scriptKind(fileName));
  const parseCheck = opts.parseErrorsOf ?? ((t: string) => parseErrors(fileName, t));
  const at = (n: ts.Node): number => n.getStart(sf);
  const lineOf = (pos: number): number => sf.getLineAndCharacterOfPosition(pos).line + 1;
  const spans: Span[] = [];
  const changes: Change[] = [];

  // The checker is built on first use: only conditions that name a const need it.
  let checker: ts.TypeChecker | undefined;
  const typeChecker = (): ts.TypeChecker => {
    if (checker === undefined) {
      const name = virtualName(fileName);
      const host: ts.CompilerHost = {
        getSourceFile: (f) => (f === name ? sf : undefined),
        getDefaultLibFileName: () => 'lib.d.ts',
        writeFile: () => undefined,
        getCurrentDirectory: () => '/',
        getCanonicalFileName: (f) => f,
        getNewLine: () => '\n',
        useCaseSensitiveFileNames: () => true,
        fileExists: (f) => f === name,
        readFile: () => undefined,
        getDirectories: () => [],
        directoryExists: () => true,
      };
      const program = ts.createProgram({
        rootNames: [name], host,
        options: { noLib: true, noResolve: true, noEmit: true, strict: true, skipLibCheck: true, types: [] },
      });
      checker = program.getTypeChecker();
    }
    return checker;
  };

  // Type-guaranteed literal condition. Only a const (or a `!` of one) counts: a let or a parameter can be narrowed to
  // `true` by the flow and still change inside a closure, and pruning it would hide a gap in the suite.
  const constOnly = (e: ts.Expression): boolean => {
    if (ts.isParenthesizedExpression(e)) return constOnly(e.expression);
    if (ts.isPrefixUnaryExpression(e) && e.operator === ts.SyntaxKind.ExclamationToken) return constOnly(e.operand);
    if (!ts.isIdentifier(e)) return false;
    const decl = typeChecker().getSymbolAtLocation(e)?.valueDeclaration;
    return decl !== undefined && ts.isVariableDeclaration(decl) && (decl.parent.flags & ts.NodeFlags.Const) !== 0;
  };
  const typeLiteral = (e: ts.Expression): boolean | null => {
    if (!constOnly(e)) return null;
    const t = typeChecker().getTypeAtLocation(e);
    if (!(t.flags & ts.TypeFlags.BooleanLiteral)) return null;
    return typeChecker().typeToString(t) === 'true';
  };
  const decide = (e: ts.Expression): { value: boolean; reason: string } | null => {
    const literal = constantOf(e);
    if (literal !== null) return { value: literal, reason: REASON_BRANCH };
    const typed = typeLiteral(e);
    return typed === null ? null : { value: typed, reason: REASON_BRANCH_TYPES };
  };

  const prunesOf = (n: ts.Node): void => {
    if (ts.isIfStatement(n)) {
      const d = decide(n.expression);
      const dead = d === null ? undefined : d.value ? n.elseStatement : n.thenStatement;
      if (d !== null && dead !== undefined) spans.push({ start: at(dead), end: dead.end, reason: d.reason });
    } else if (ts.isWhileStatement(n)) {
      const d = decide(n.expression);
      if (d !== null && !d.value) spans.push({ start: at(n.statement), end: n.statement.end, reason: d.reason });
    } else if (ts.isExpressionStatement(n) && ts.isCallExpression(n.expression)) {
      const name = dotted(n.expression.expression);
      if (name === 'console.assert') spans.push({ start: at(n), end: n.end, reason: REASON_ASSERT });
      else if (name !== null && LOG_PREFIXES.some((p) => name.startsWith(p))) {
        spans.push({ start: at(n), end: n.end, reason: REASON_LOG });
      }
    }
  };

  const push = (pos: number, operator: Operator, description: string, edits: Edit[]): void => {
    const line = lineOf(pos);
    if (lines.has(line)) changes.push({ line, pos, operator, description, edits });
  };
  const replaceNode = (n: ts.Node, text: string): Edit => ({ start: at(n), end: n.end, text });

  const changesOf = (n: ts.Node): void => {
    if (ts.isBinaryExpression(n)) {
      const op = ts.tokenToString(n.operatorToken.kind) ?? '';
      const token: Edit = { start: n.operatorToken.getStart(sf), end: n.operatorToken.end, text: '' };
      const operatorEdit = (replacement: string, wrap: boolean): Edit[] => {
        const edits: Edit[] = [{ ...token, text: replacement }];
        if (wrap) {  // `**` binds tighter than `*`: the whole node goes in parentheses so the change applies to it
          edits.push({ start: at(n), end: at(n), text: '(' }, { start: n.end, end: n.end, text: ')' });
        }
        return edits;
      };
      const arithmetic = ARITHMETIC.get(op);
      const assign = ARITHMETIC_ASSIGN.get(op);
      const comparison = COMPARISON.get(op);
      if (arithmetic !== undefined) {
        push(token.start, 'arithmetic', `arithmetic operator ${op} changed to ${arithmetic}`,
          operatorEdit(arithmetic, op === '**'));
      } else if (assign !== undefined) {
        push(token.start, 'arithmetic', `arithmetic operator ${op} changed to ${assign}`, operatorEdit(assign, false));
      } else if (comparison !== undefined) {
        push(token.start, 'comparison', `comparison ${op} changed to ${comparison}`, operatorEdit(comparison, false));
      }
    }
    const condition = conditionOf(n);
    if (condition !== null) {
      const [e, kind] = condition;
      const original = sf.text.slice(at(e), e.end);
      push(at(e), 'negate_condition', `condition of ${kind} negated`, [replaceNode(e, `(!(${original}))`)]);
    }
    if (ts.isNumericLiteral(n) && Number.isFinite(Number(n.text))) {
      const v = Number(n.text);
      const shown = (x: number): string => (x < 0 ? `(${x})` : String(x));
      push(at(n), 'constant', 'numeric constant changed: +1', [replaceNode(n, shown(v + 1))]);
      push(at(n), 'constant', 'numeric constant changed: -1', [replaceNode(n, shown(v - 1))]);
    } else if (n.kind === ts.SyntaxKind.TrueKeyword || n.kind === ts.SyntaxKind.FalseKeyword) {
      push(at(n), 'constant', 'boolean inverted', [replaceNode(n, n.kind === ts.SyntaxKind.TrueKeyword ? 'false' : 'true')]);
    } else if ((ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) && !isLooseString(n)) {
      const nonEmpty = n.text.length > 0;
      push(at(n), 'constant', nonEmpty ? 'non-empty string changed to empty' : 'empty string changed to non-empty',
        [replaceNode(n, nonEmpty ? "''" : "'a2dlc'")]);
    } else if (ts.isReturnStatement(n) && n.expression !== undefined
      && !(ts.isIdentifier(n.expression) && n.expression.text === 'undefined')) {
      push(at(n), 'return_none', 'return <value> changed to return undefined', [replaceNode(n, 'return undefined;')]);
    } else if (ts.isExpressionStatement(n) && (ts.isCallExpression(n.expression)
      || (ts.isAwaitExpression(n.expression) && ts.isCallExpression(n.expression.expression)))) {  // `await p;` is not a call
      push(at(n), 'remove_call', 'call used as a statement removed', [replaceNode(n, ';')]);
    }
  };

  const visit = (n: ts.Node): void => {
    if (skipped(n)) return;
    prunesOf(n);
    changesOf(n);
    ts.forEachChild(n, visit);
  };
  visit(sf);

  changes.sort((a, b) => a.line - b.line || a.pos - b.pos
    || (a.operator < b.operator ? -1 : a.operator > b.operator ? 1 : 0));

  const original = Buffer.from(text, 'utf8');
  const mutants: Mutant[] = [];
  const pruned: Pruned[] = [];
  const uncompilable: { line: number; description: string }[] = [];
  for (const c of changes) {
    const span = spans.find((s) => c.pos >= s.start && c.pos < s.end);  // the outermost prune wins: pre-order
    if (span !== undefined) {
      if (!pruned.some((p) => p.line === c.line && p.reason === span.reason)) pruned.push({ line: c.line, reason: span.reason });
      continue;
    }
    const bytes = splice(text, original, c.edits);
    if (bytes.equals(original)) continue;
    if (parseCheck(bytes.toString('utf8')).length > 0) {
      uncompilable.push({ line: c.line, description: c.description });
      continue;
    }
    mutants.push({ line: c.line, operator: c.operator, description: c.description, bytes });
  }
  return { mutants, pruned, uncompilable };
}

export interface MutateRequest {
  candidate_dir: string;
  changed_files: string[];
  budget?: { changed_lines?: Record<string, number[]>; max_mutants?: number };
}

interface Response {
  schema_version: 1;
  mutants: { id: string; file: string; line: number; operator: Operator; description: string }[];
  not_generated: { file: string; reason: string }[];
  pruned: { file: string; line: number; reason: string }[];
  error: string | null;
}

const clip = (s: string): string => s.slice(0, 300);

// Operation mutate: writes each mutant as the whole mutated file at <io>/mutants/<id>/<file> and the response at
// <io>/response.json. Errors go to the response's error field, with no mutants, so the core knows nothing was measured.
export function runMutate(req: MutateRequest, io: string): void {
  const budget = req.budget ?? {};
  const requested = budget.changed_lines ?? {};
  const limit = Number(budget.max_mutants) || 30;
  const response: Response = { schema_version: 1, mutants: [], not_generated: [], pruned: [], error: null };
  const pending: { id: string; file: string; bytes: Buffer }[] = [];
  try {
    for (const file of req.changed_files) {
      const lines = new Set<number>(requested[file] ?? []);
      const path = join(req.candidate_dir, file);
      if (lines.size === 0 || !existsSync(path)) continue;
      if (!TS_FILE.test(file)) {
        response.not_generated.push({ file, reason: 'not TypeScript: this engine does not mutate it' });
        continue;
      }
      const raw = readFileSync(path);
      let text: string;
      try {
        text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(raw);
      } catch (e) {
        response.not_generated.push({ file, reason: clip(`unsupported encoding: ${String(e)}`) });
        continue;
      }
      const syntax = parseErrors(file, text);
      if (syntax.length > 0) {
        response.not_generated.push({ file, reason: clip(`does not parse: ${syntax[0]}`) });
        continue;
      }
      const analysis = mutantsOf(text, file, lines);
      response.pruned.push(...analysis.pruned.map((p) => ({ file, line: p.line, reason: p.reason })));
      for (const u of analysis.uncompilable) {
        response.not_generated.push({ file, reason: clip(`does not compile: ${u.description} at line ${u.line}`) });
      }
      const own = analysis.mutants;
      const fits = own.slice(0, Math.max(limit - response.mutants.length, 0));
      for (const m of fits) {
        const id = `m${String(response.mutants.length + 1).padStart(3, '0')}`;
        pending.push({ id, file, bytes: m.bytes });
        response.mutants.push({ id, file, line: m.line, operator: m.operator, description: m.description });
      }
      if (own.length > fits.length) {
        const rest = [...new Set(own.slice(fits.length).map((m) => m.line))].sort((a, b) => a - b);
        response.not_generated.push({
          file,
          reason: clip(`max_mutants=${limit}: ${own.length - fits.length} mutants not generated (lines ${rest.join(', ')})`),
        });
      }
    }
    // Files are written only once every changed file was analysed: a failure above leaves no mutant on disk.
    for (const p of pending) {
      const dest = join(io, 'mutants', p.id, p.file);
      mkdirSync(dirname(dest), { recursive: true });
      writeFileSync(dest, p.bytes);
    }
  } catch (e) {
    response.mutants = [];
    response.not_generated = [];
    response.pruned = [];
    response.error = String(e);
    rmSync(join(io, 'mutants'), { recursive: true, force: true });
  }
  writeFileSync(join(io, 'response.json'), JSON.stringify(response, null, 1), 'utf-8');
}
