// Tier 0 of the TypeScript engine (protocol §5, Stage 3): compile, typecheck and policy of the changed files in the
// candidate, plus the blocking probes of budget.probes. Same contract as the Python engine: only findings that the base
// of the same file did not have count, so what was already wrong belongs to the Builder and cannot jam the loop.
// A tool that does not run is a "tooling" finding: what was not measured does not count as clean.
import ts from 'typescript';
import { existsSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

export type Kind = 'compile' | 'typecheck' | 'lint' | 'probe' | 'policy' | 'header' | 'tooling';
export interface Finding { kind: Kind; file: string; line: number | null; message: string }
export interface Probe { id?: string; file: string; expr: string }
export interface Policy {
  forbidden_imports: string[];
  require_header: boolean;
  max_complexity: number;
  // Not transported yet: the contract's tier0_policy has no such field (see README, open items). Read when present.
  forbidden_side_effects: string[];
}
export interface Tier0Request {
  base_dir: string;
  candidate_dir: string;
  changed_files: string[];
  budget?: { probes?: Probe[]; policy?: unknown };
}
export interface Tier0Options {
  probeTimeoutMs?: number;
  createProgram?: (config: ts.CreateProgramOptions) => ts.Program;
}
export interface Tier0Result { ok: boolean; findings: Finding[]; error: string | null }

export const TIMEOUT_PROBE_MS = 30_000;
export const MAX_MESSAGE = 300;
export const ENGINE_DIR = path.dirname(fileURLToPath(import.meta.url));
export const SIDE_EFFECTS = ['network.http.outbound', 'fs.write', 'db.raw_sql_unparameterized', 'time.system_clock_direct'];
export const TARGET_FILE = /\.(ts|tsx|mts|cts|js|mjs|cjs)$/;
const TYPED_FILE = /\.(ts|tsx|mts|cts)$/;

// Compiler options of the check. No node_modules exist under --network none, so bare specifiers do not resolve and
// their bindings are `any`; that is the intended behaviour, and TS2307 for bare specifiers is dropped below.
export const COMPILER_OPTIONS: ts.CompilerOptions = {
  strict: true,
  noEmit: true,
  skipLibCheck: true,
  allowJs: true,
  checkJs: false,
  allowImportingTsExtensions: true,
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
  jsx: ts.JsxEmit.Preserve,
  types: [],
};

// Diagnostics that only say a package or a Node global is not installed in the image: not the Builder's code.
// TS2307 (cannot find module) is dropped for bare specifiers only; a missing relative file is a real error.
const NOT_INSTALLED = new Set([2580, 2581, 2582, 2584, 2591, 2592, 2593, 2792, 7016]);

const LOGICAL = new Set([ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken]);
const WRITE_CALLS = new Set(['writeFile', 'writeFileSync', 'appendFile', 'appendFileSync', 'createWriteStream', 'mkdir',
  'mkdirSync', 'rm', 'rmSync', 'unlink', 'unlinkSync', 'rename', 'renameSync', 'copyFile', 'copyFileSync', 'truncate',
  'truncateSync', 'writeSync', 'cp', 'cpSync']);
const HTTP_ROOTS = new Set(['http', 'https', 'axios', 'got', 'undici', 'needle', 'superagent']);
const RAW_SQL_CALLS = new Set(['query', 'execute', 'raw']);
const UNSAFE_CALLS = new Set(['$queryRawUnsafe', '$executeRawUnsafe']);

// The Python policy's defaults (contracts/defs.schema.json#/$defs/tier0_policy).
export function policyOf(requested: unknown): Policy {
  const r = (requested ?? {}) as Record<string, unknown>;
  const strings = (v: unknown, allowed?: string[]) =>
    Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && (!allowed || allowed.includes(x))) : [];
  return {
    forbidden_imports: strings(r.forbidden_imports),
    require_header: r.require_header === true,
    max_complexity: Number.isInteger(r.max_complexity) && (r.max_complexity as number) >= 1 ? (r.max_complexity as number) : 15,
    forbidden_side_effects: strings(r.forbidden_side_effects, SIDE_EFFECTS),
  };
}

export function scriptKind(file: string): ts.ScriptKind {
  if (file.endsWith('.tsx')) return ts.ScriptKind.TSX;
  if (/\.(ts|mts|cts)$/.test(file)) return ts.ScriptKind.TS;
  return ts.ScriptKind.JS;
}

export function parse(text: string, file: string): ts.SourceFile {
  return ts.createSourceFile(file, text, ts.ScriptTarget.ES2022, true, scriptKind(file));
}

// True when the text has no syntax errors (transpileModule reports only syntactic diagnostics here).
export function parses(text: string, file: string): boolean {
  const out = ts.transpileModule(text, { fileName: file, reportDiagnostics: true, compilerOptions: COMPILER_OPTIONS });
  return (out.diagnostics ?? []).length === 0;
}

export function lineOf(sf: ts.SourceFile, pos: number): number {
  return sf.getLineAndCharacterOfPosition(pos).line + 1;
}

const clip = (s: string) => s.slice(0, MAX_MESSAGE);

// The message of a diagnostic with the absolute roots replaced: base and candidate live in different trees, and a
// path in the text would make every error look new.
export function messageOf(d: ts.Diagnostic, roots: string[]): string {
  let m = ts.flattenDiagnosticMessageText(d.messageText, ' ').replace(/\\/g, '/');
  for (const r of roots) m = m.split(r.replace(/\\/g, '/')).join('<root>');
  return clip(m);
}

function specifierOf(d: ts.Diagnostic): string {
  if (!d.file || d.start === undefined) return '';
  return d.file.text.slice(d.start, d.start + (d.length ?? 0)).replace(/^['"`]|['"`]$/g, '');
}

// Node globals that no type declares in the image (no @types/node under --network none). TS2304 (cannot find name) and
// TS2503 (cannot find namespace) are dropped only when they name one of these: any other unknown name is a finding.
export const NODE_GLOBALS = new Set([
  '__dirname', '__filename', 'exports', 'module', 'require', 'NodeJS', 'global', 'process', 'Buffer',
  'setImmediate', 'clearImmediate',
]);
const UNKNOWN_NAME = new Set([2304, 2503]);

export function dropped(d: ts.Diagnostic): boolean {
  if (d.code === 2307) return !/^(\.{1,2}\/|\/)/.test(specifierOf(d));
  if (UNKNOWN_NAME.has(d.code)) return NODE_GLOBALS.has(specifierOf(d));
  return NOT_INSTALLED.has(d.code);
}

// The findings of `current` that `previous` (the base of the same file) did not have, matched by message.
export function onlyNew<T extends { message: string }>(current: T[], previous: string[]): T[] {
  const counts = new Map<string, number>();
  for (const m of previous) counts.set(m, (counts.get(m) ?? 0) + 1);
  return current.filter((x) => {
    const c = counts.get(x.message) ?? 0;
    if (c > 0) {
      counts.set(x.message, c - 1);
      return false;
    }
    return true;
  });
}

export function tooling(file: string, message: string): Finding {
  return { kind: 'tooling', file, line: null, message: clip(message) };
}

// ---------- policy (the rules of engine.py's policy_violations, on the TypeScript AST) ----------

export interface Violation { line: number; message: string }

const norm = (spec: string) => (spec.startsWith('node:') ? spec.slice(5) : spec);

// The forbidden entry that covers the module ("fs" covers "fs", "node:fs", "fs/promises"), or null.
export function forbiddenBy(spec: string, forbidden: string[]): string | null {
  const n = norm(spec);
  for (const p of forbidden) {
    const q = norm(p);
    if (n === q || n.startsWith(q + '/')) return p;
  }
  return null;
}

function unparen(e: ts.Expression): ts.Expression {
  let x = e;
  while (ts.isParenthesizedExpression(x)) x = x.expression;
  return x;
}

function literalText(e: ts.Node | undefined): string | null {
  if (!e) return null;
  return ts.isStringLiteral(e) || ts.isNoSubstitutionTemplateLiteral(e) ? e.text : null;
}

function constantTrue(e: ts.Expression): boolean {
  const x = unparen(e);
  if (x.kind === ts.SyntaxKind.TrueKeyword) return true;
  if (ts.isNumericLiteral(x)) return Number(x.text) !== 0;
  if (ts.isStringLiteral(x) || ts.isNoSubstitutionTemplateLiteral(x)) return x.text !== '';
  return false;
}

// Whether a loop body can leave the loop: an unlabeled break counts unless it sits in a nested loop or switch (where it
// leaves that one); a labeled break counts anywhere except inside a function, unless its label is declared inside the
// body (then it leaves a block of the body, not the loop).
export function exitsLoop(body: ts.Node): boolean {
  const innerLabels = new Set<string>();
  const collect = (n: ts.Node): void => {
    if (ts.isLabeledStatement(n)) innerLabels.add(n.label.text);
    ts.forEachChild(n, collect);
  };
  collect(body);
  let found = false;
  const walk = (n: ts.Node, nested: boolean): void => {
    if (found) return;
    if (ts.isBreakStatement(n)) {
      if (n.label ? !innerLabels.has(n.label.text) : !nested) found = true;
      return;
    }
    if (ts.isFunctionLike(n) || ts.isClassLike(n)) return;
    const inner = nested || ts.isIterationStatement(n, false) || ts.isSwitchStatement(n);
    ts.forEachChild(n, (c) => walk(c, inner));
  };
  walk(body, false);
  return found;
}

function countedName(n: ts.Node, sf: ts.SourceFile): string | null {
  if (ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n) || ts.isGetAccessorDeclaration(n) || ts.isSetAccessorDeclaration(n)) {
    if (!n.body) return null;
    return n.name ? n.name.getText(sf) : 'default';
  }
  if (ts.isConstructorDeclaration(n)) return n.body ? 'constructor' : null;
  if (ts.isArrowFunction(n) || ts.isFunctionExpression(n)) {
    const p = n.parent;
    if (ts.isVariableDeclaration(p) && ts.isIdentifier(p.name)) return p.name.text;
    if (ts.isPropertyAssignment(p)) return p.name.getText(sf);
  }
  return null;
}

// McCabe, as the Python engine: 1 plus each decision point; nested functions count toward the enclosing one.
export function cyclomatic(fn: ts.Node): number {
  let c = 1;
  const walk = (x: ts.Node): void => {
    if (ts.isIfStatement(x) || ts.isConditionalExpression(x) || ts.isIterationStatement(x, false) ||
        ts.isCatchClause(x) || ts.isCaseClause(x)) c++;
    else if (ts.isBinaryExpression(x) && LOGICAL.has(x.operatorToken.kind)) c++;
    ts.forEachChild(x, walk);
  };
  ts.forEachChild(fn, walk);
  return c;
}

// Whether an expression denotes eval however it is written: `eval`, `(0, eval)`, `[eval][0]`, `globalThis['eval']`.
function isEvalRef(e: ts.Expression): boolean {
  const x = unparen(e);
  if (ts.isIdentifier(x)) return x.text === 'eval';
  if (ts.isBinaryExpression(x) && x.operatorToken.kind === ts.SyntaxKind.CommaToken) return isEvalRef(x.right);
  if (ts.isPropertyAccessExpression(x)) return x.name.text === 'eval';
  if (ts.isElementAccessExpression(x)) {
    if (literalText(x.argumentExpression) === 'eval') return true;
    const obj = unparen(x.expression);
    return ts.isArrayLiteralExpression(obj) && obj.elements.some((el) => isEvalRef(el));
  }
  return false;
}

function calleeName(e: ts.Expression): string | null {
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) return e.name.text;
  return null;
}

function rootName(e: ts.Expression): string | null {
  let x = unparen(e);
  while (ts.isPropertyAccessExpression(x)) x = unparen(x.expression);
  return ts.isIdentifier(x) ? x.text : null;
}

function unsafeSqlArg(a: ts.Expression | undefined): boolean {
  return !!a && (ts.isTemplateExpression(a) || (ts.isBinaryExpression(a) && a.operatorToken.kind === ts.SyntaxKind.PlusToken));
}

export function policyViolations(sf: ts.SourceFile, policy: Policy): Violation[] {
  const out: Violation[] = [];
  const effects = new Set(policy.forbidden_side_effects);
  const add = (n: ts.Node, message: string) => out.push({ line: lineOf(sf, n.getStart(sf)), message });
  const checkModule = (n: ts.Node, spec: string) => {
    const p = forbiddenBy(spec, policy.forbidden_imports);
    if (p) add(n, `forbidden import per policy: ${spec} (${p})`);
  };
  const sideEffect = (n: ts.Node, effect: string, detail: string) => {
    if (effects.has(effect)) add(n, `forbidden side effect per boundary: ${effect} (${clip(detail).slice(0, 80)})`);
  };
  const callChecks = (n: ts.CallExpression) => {
    const callee = unparen(n.expression);
    if (callee.kind === ts.SyntaxKind.ImportKeyword) {
      const spec = literalText(n.arguments[0]);
      if (spec !== null) checkModule(n, spec);
      else add(n, 'reflection: dynamic import with a non-literal specifier');
      return;
    }
    if (ts.isIdentifier(callee)) {
      if (callee.text === 'require') {
        const spec = literalText(n.arguments[0]);
        if (spec !== null) checkModule(n, spec);
        else add(n, 'reflection: dynamic require with a non-literal specifier');
      } else if (callee.text === 'eval' || callee.text === 'Function') {
        add(n, `reflection: call to ${callee.text}`);
      }
    } else if (isEvalRef(callee)) {
      add(n, 'reflection: call to eval');
    }
    const name = calleeName(callee);
    const root = rootName(callee);
    const text = callee.getText(sf);
    if (root === 'Date' && name === 'now' && ts.isPropertyAccessExpression(callee)) sideEffect(n, 'time.system_clock_direct', 'Date.now()');
    if (name !== null && WRITE_CALLS.has(name)) sideEffect(n, 'fs.write', text);
    if ((ts.isIdentifier(callee) && callee.text === 'fetch') || (root !== null && HTTP_ROOTS.has(root))) {
      sideEffect(n, 'network.http.outbound', text);
    }
    if (name !== null && (UNSAFE_CALLS.has(name) || (RAW_SQL_CALLS.has(name) && unsafeSqlArg(n.arguments[0])))) {
      sideEffect(n, 'db.raw_sql_unparameterized', text);
    }
  };
  const visit = (n: ts.Node): void => {
    if (ts.isImportDeclaration(n) && ts.isStringLiteral(n.moduleSpecifier)) checkModule(n, n.moduleSpecifier.text);
    else if (ts.isExportDeclaration(n) && n.moduleSpecifier && ts.isStringLiteral(n.moduleSpecifier)) checkModule(n, n.moduleSpecifier.text);
    else if (ts.isImportEqualsDeclaration(n) && ts.isExternalModuleReference(n.moduleReference) &&
             ts.isStringLiteral(n.moduleReference.expression)) checkModule(n, n.moduleReference.expression.text);
    else if (ts.isCallExpression(n)) callChecks(n);
    else if (ts.isNewExpression(n) && ts.isIdentifier(n.expression)) {
      const ctor = n.expression.text;
      if (ctor === 'Function') add(n, 'reflection: call to Function');
      if (ctor === 'Date' && (n.arguments ?? []).length === 0) sideEffect(n, 'time.system_clock_direct', 'new Date()');
      if (ctor === 'WebSocket') sideEffect(n, 'network.http.outbound', 'new WebSocket');
    } else if (ts.isWhileStatement(n) && constantTrue(n.expression) && !exitsLoop(n.statement)) {
      add(n, 'unbounded loop: while over a true constant with no break');
    } else if (ts.isDoStatement(n) && constantTrue(n.expression) && !exitsLoop(n.statement)) {
      add(n, 'unbounded loop: do-while over a true constant with no break');
    } else if (ts.isForStatement(n) && (!n.condition || constantTrue(n.condition)) && !exitsLoop(n.statement)) {
      add(n, 'unbounded loop: for without a condition (or a true one) and no break');
    }
    const name = countedName(n, sf);
    if (name !== null) {
      const c = cyclomatic(n);
      if (c > policy.max_complexity) add(n, `cyclomatic complexity ${c} of ${name} exceeds max_complexity ${policy.max_complexity}`);
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out.sort((a, b) => a.line - b.line);
}

// ---------- probes (budget.probes): run per probe in its own process, 30 s by default ----------

// Evaluates the probe expression with the module's exports and raises(ExcCtor, fn, ...args) in scope. The expression
// may use TypeScript syntax: it is transpiled before the evaluation. Exit 0 passes, 3 means it returned false, any
// other status means it raised (the last stderr line names the error).
const PROBE_RUNNER = `
import { pathToFileURL } from 'node:url';
import { stripTypeScriptTypes } from 'node:module';
const [file, expr] = process.argv.slice(1);
try {
  const mod = await import(pathToFileURL(file).href);
  // A CommonJS candidate exposes its exports under default: they are merged in as the named exports.
  const named = { ...(mod.default && typeof mod.default === 'object' ? mod.default : {}), ...mod };
  delete named.default;
  const raises = (exc, f, ...args) => {
    try { f(...args); } catch (e) { if (e instanceof exc) return true; throw e; }
    return false;
  };
  Object.assign(globalThis, named, { raises });
  const value = (0, eval)(stripTypeScriptTypes('(' + expr + ')'));
  process.exitCode = value ? 0 : 3;
} catch (e) {
  console.error('A2DLC_PROBE_ERROR ' + (e && e.name ? e.name : 'Error') + ': ' + (e && e.message ? e.message : String(e)));
  process.exitCode = 1;
}
`;

function lastLine(text: string): string {
  const lines = text.split('\n').map((l) => l.trim()).filter((l) => l.length > 0);
  return lines.length ? lines[lines.length - 1] : '';
}

export function runProbe(p: Probe, candidateDir: string, timeoutMs: number): Finding | null {
  const file = path.join(candidateDir, p.file);
  const r = spawnSync(process.execPath, ['--no-warnings', '--import', 'tsx', '--input-type=module', '-e', PROBE_RUNNER, file, p.expr], {
    cwd: ENGINE_DIR, encoding: 'utf-8', timeout: timeoutMs, maxBuffer: 1 << 20,
  });
  let reason: string;
  if (r.signal || (r.error as NodeJS.ErrnoException | undefined)?.code === 'ETIMEDOUT') {
    reason = `exceeded ${Math.round(timeoutMs / 1000)} s`;
  } else if (r.status === 0) {
    return null;
  } else if (r.status === 3) {
    reason = 'returned false';
  } else {
    const marked = (r.stderr ?? '').split('\n').find((l) => l.startsWith('A2DLC_PROBE_ERROR '));
    const last = marked ? marked.slice('A2DLC_PROBE_ERROR '.length) : lastLine(`${r.stderr ?? ''}\n${r.stdout ?? ''}`) || `rc=${r.status}`;
    reason = `raised ${last}`;
  }
  return { kind: 'probe', file: p.file, line: null, message: clip(`${p.id ?? '?'}: ${p.expr} ${reason}`) };
}

// ---------- the Tier 0 run ----------

function policyFindings(
  file: string, candSf: ts.SourceFile, baseSf: ts.SourceFile | undefined, policy: Policy,
): Finding[] {
  const now = policyViolations(candSf, policy).map((v) => ({ kind: 'policy' as Kind, file, line: v.line, message: v.message }));
  const before = baseSf ? policyViolations(baseSf, policy).map((v) => v.message) : [];
  return onlyNew(now, before);
}

export function runTier0(req: Tier0Request, options: Tier0Options = {}): Tier0Result {
  const base = req.base_dir;
  const cand = req.candidate_dir;
  const policy = policyOf(req.budget?.policy);
  const probes = (req.budget?.probes ?? []).filter((p) => p.file && p.expr);
  const files = req.changed_files.filter((f) => TARGET_FILE.test(f) && existsSync(path.join(cand, f)));
  const findings: Finding[] = [];

  if (files.length) {
    const roots = files.flatMap((f) => (existsSync(path.join(base, f)) ? [path.join(cand, f), path.join(base, f)] : [path.join(cand, f)]));
    const normRoots = [base, cand];
    let program: ts.Program | undefined;
    try {
      const create = options.createProgram ?? ts.createProgram;
      program = create({ rootNames: roots, options: COMPILER_OPTIONS });
    } catch (e) {
      findings.push(tooling(files[0], `TypeScript program did not build (no typecheck in this round): ${(e as Error).message}`));
    }
    if (program) {
      const prog = program;
      const typed: string[] = [];
      for (const f of files) {
        const candSf = prog.getSourceFile(path.join(cand, f));
        if (!candSf) {
          findings.push(tooling(f, 'file missing from the TypeScript program: not checked'));
          continue;
        }
        const baseSf = existsSync(path.join(base, f)) ? prog.getSourceFile(path.join(base, f)) : undefined;
        const compileNow = prog.getSyntacticDiagnostics(candSf).map((d) => ({
          kind: 'compile' as Kind, file: f, line: d.start === undefined ? null : lineOf(candSf, d.start), message: messageOf(d, normRoots),
        }));
        const compileBefore = baseSf ? prog.getSyntacticDiagnostics(baseSf).map((d) => messageOf(d, normRoots)) : [];
        const compiles = onlyNew(compileNow, compileBefore);
        findings.push(...compiles, ...policyFindings(f, candSf, baseSf, policy));
        if (policy.require_header && !candSf.text.includes('@a2dlc:slice_id')) {
          findings.push({ kind: 'header', file: f, line: 1, message: 'missing header @a2dlc:slice_id (require_header)' });
        }
        if (!compiles.length && TYPED_FILE.test(f)) typed.push(f);
      }
      try {
        for (const f of typed) {
          const candSf = prog.getSourceFile(path.join(cand, f))!;
          const baseSf = existsSync(path.join(base, f)) ? prog.getSourceFile(path.join(base, f)) : undefined;
          const now = prog.getSemanticDiagnostics(candSf).filter((d) => !dropped(d)).map((d) => ({
            kind: 'typecheck' as Kind, file: f, line: d.start === undefined ? null : lineOf(candSf, d.start), message: messageOf(d, normRoots),
          }));
          const before = baseSf ? prog.getSemanticDiagnostics(baseSf).filter((d) => !dropped(d)).map((d) => messageOf(d, normRoots)) : [];
          findings.push(...onlyNew(now, before));
        }
      } catch (e) {
        findings.push(tooling(typed[0] ?? files[0], `typecheck did not run (no typecheck in this round): ${(e as Error).message}`));
      }
    }
  }

  if (probes.length) {
    const timeout = options.probeTimeoutMs ?? TIMEOUT_PROBE_MS;
    for (const p of probes) {
      const f = runProbe(p, cand, timeout);
      if (f) findings.push(f);
    }
  }
  return { ok: findings.length === 0, findings, error: null };
}

// Writes /io/response.json (contracts/engine-tier0-response.schema.json). An unexpected crash is an error response,
// not a verdict: ok and findings say nothing then.
export function main(req: Tier0Request, io: string): void {
  let body: Record<string, unknown>;
  try {
    const r = runTier0(req);
    body = { schema_version: 1, ok: r.ok, findings: r.findings, error: null };
  } catch (e) {
    body = { schema_version: 1, ok: false, findings: [], error: `tier0 crashed: ${clip((e as Error).message ?? String(e))}` };
  }
  writeFileSync(`${io}/response.json`, JSON.stringify(body, null, 1));
}
