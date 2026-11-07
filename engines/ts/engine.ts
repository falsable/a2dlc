// a2dlc TypeScript engine: differential check with fast-check over the exported functions that changed.
// Contract: reads /io/request.json and writes /io/response.json (contracts/engine-*.schema.json).
//
// Targets: exported functions whose text changed, plus those that use (within the file, transitively) a top-level
// declaration that changed. So a change in a helper that is not isExported, or in a constant, counts.
// Generators (typed_default): derived from the parameter types, plus the literals of both versions of the file
// and their boundaries (n±1, n/100, strings of length n). Override: .a2dlc/arbs.ts with
// `export default (fc) => ({ "<function>": fc.tuple(...) })`, without importing fast-check (the repo does not have it).
// Async: a promise result is awaited. Network, filesystem writes and timers are trapped while a target runs; a target that
// touches one, returns a server or a function, or has a call that never settles is INCONCLUSIVE with that reason (each target
// has its own status).
import fc from 'fast-check';
import ts from 'typescript';
import { isDeepStrictEqual } from 'node:util';
import fs, { existsSync, readFileSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import timers from 'node:timers';
import timersPromises from 'node:timers/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { createRequire, syncBuiltinESMExports } from 'node:module';

type Json = Record<string, unknown>;
const IO = process.env.A2DLC_IO ?? '/io';
const req = JSON.parse(readFileSync(`${IO}/request.json`, 'utf-8'));
const budget = req.budget ?? {};
const EXAMPLES: number = budget.examples ?? 2000;
const SEED: number = budget.seed ?? 42;
const FC_VERSION: string = createRequire(import.meta.url)('fast-check/package.json').version;

// ---------- target discovery ----------

// The code of a node without comments and layout: the printer writes it from the AST, so two texts that print the same parse
// to the same tree. Shortcut: the printer keeps whether a block was written on one line or on several, a literal keeps its
// source spelling ('a' and "a" differ) and type annotations stay in, so those changes are judged as before; upgrade to a
// token comparison only if a measured case needs them folded.
const printer = ts.createPrinter({ removeComments: true });
const codeOf = (n: ts.Node, sf: ts.SourceFile) => printer.printNode(ts.EmitHint.Unspecified, n, sf);
const linesOf = (n: ts.Node, sf: ts.SourceFile): [number, number] =>
  [sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, sf.getLineAndCharacterOfPosition(n.getEnd()).line + 1];

interface Decl {
  name: string;
  text: string; // codeOf: what targetsOf compares
  raw: string; // the source text, comments included
  lines: [number, number];
  exported: boolean;
  isFunction: boolean;
  refs: Set<string>;
}

function topLevel(sf: ts.SourceFile): Map<string, Decl> {
  const out = new Map<string, Decl>();
  const exportedNames = new Set<string>();
  const refsOf = (n: ts.Node) => {
    const s = new Set<string>();
    const walk = (x: ts.Node) => {
      if (ts.isIdentifier(x)) s.add(x.text);
      ts.forEachChild(x, walk);
    };
    walk(n);
    return s;
  };
  const isExported = (n: ts.Node) =>
    !!(ts.getCombinedModifierFlags(n as ts.Declaration) & ts.ModifierFlags.Export);
  for (const st of sf.statements) {
    if (ts.isExportDeclaration(st) && st.exportClause && ts.isNamedExports(st.exportClause)) {
      for (const e of st.exportClause.elements) exportedNames.add((e.propertyName ?? e.name).text);
      continue;
    }
    const add = (name: string, node: ts.Node, isFunction: boolean) =>
      out.set(name, { name, text: codeOf(node, sf), raw: node.getText(sf), lines: linesOf(node, sf), exported: isExported(node), isFunction, refs: refsOf(node) });
    if (ts.isFunctionDeclaration(st) && st.name) add(st.name.text, st, true);
    else if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) {
        if (!ts.isIdentifier(d.name)) continue;
        const init = d.initializer;
        const fn = !!init && (ts.isArrowFunction(init) || ts.isFunctionExpression(init));
        out.set(d.name.text, { name: d.name.text, text: codeOf(st, sf), raw: st.getText(sf), lines: linesOf(st, sf), exported: isExported(st), isFunction: fn, refs: refsOf(d) });
      }
    } else if (ts.isClassDeclaration(st) && st.name) add(st.name.text, st, false);
    else if (ts.isEnumDeclaration(st) || ts.isTypeAliasDeclaration(st) || ts.isInterfaceDeclaration(st)) add(st.name.text, st, false);
  }
  for (const n of exportedNames) {
    const d = out.get(n);
    if (d) d.exported = true;
  }
  return out;
}

function targetsOf(baseSf: ts.SourceFile, candSf: ts.SourceFile): string[] {
  const b = topLevel(baseSf);
  const c = topLevel(candSf);
  const changed = new Set<string>();
  for (const [name, d] of c) if (b.get(name)?.text !== d.text) changed.add(name);
  for (const name of b.keys()) if (!c.has(name)) changed.add(name);
  // Closure: whoever uses something that changed also changed its possible behaviour.
  let grew = true;
  while (grew) {
    grew = false;
    for (const [name, d] of c) {
      if (changed.has(name)) continue;
      for (const r of d.refs) if (changed.has(r) && r !== name) { changed.add(name); grew = true; break; }
    }
  }
  return [...c.values()].filter((d) => d.exported && d.isFunction && changed.has(d.name) && b.get(d.name)?.isFunction).map((d) => d.name);
}

// The exported functions whose source text changed but whose code did not (a comment, the layout): nothing to run, they go out
// as PASS with EQUIVALENT_AST and their own lines. A function that targetsOf already returns (it reads something that
// changed) is judged, not listed here.
const EQUIVALENT_AST = 'equivalent by AST: the code is the same without comments and layout, so the engine did not run it';
function equivalentOf(baseSf: ts.SourceFile, candSf: ts.SourceFile): Decl[] {
  const b = topLevel(baseSf);
  const judged = new Set(targetsOf(baseSf, candSf));
  return [...topLevel(candSf).values()].filter((d) => {
    const old = b.get(d.name);
    return d.exported && d.isFunction && old?.isFunction && !judged.has(d.name) && old.text === d.text && old.raw !== d.raw;
  });
}

// ---------- generadores ----------

function harvest(sfs: ts.SourceFile[]) {
  const strs = new Set<string>();
  const nums = new Set<number>();
  for (const sf of sfs) {
    const walk = (n: ts.Node) => {
      if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) strs.add(n.text);
      else if (ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n)) strs.add(n.text);
      else if (ts.isNumericLiteral(n)) nums.add(Number(n.text));
      ts.forEachChild(n, walk);
    };
    walk(sf);
  }
  return { strs: [...strs].filter((s) => s.length <= 40).slice(0, 80), nums: [...nums].filter(Number.isFinite).slice(0, 40) };
}

function stringArb(literals: { strs: string[]; nums: number[] }): fc.Arbitrary<string> {
  const frag = literals.strs.length ? fc.constantFrom(...literals.strs) : fc.string({ maxLength: 6 });
  const sep = fc.constantFrom('/', '//', '#', '?', '&', '=', ' ', '.', '-', '_');
  const variant = fc.tuple(frag, fc.nat(5)).map(([s, k]) => [s, s.toUpperCase(), `${s}/`, `${s}//`, `/${s}`, ` ${s} `][k]);
  // v2: the soup is built from variants (not only from literals as they are), so an uppercase letter or an extra
  // slash can land in the middle of a composition.
  const soup = fc.array(fc.oneof(variant, sep, fc.string({ maxLength: 3 })), { minLength: 1, maxLength: 4 }).map((a) => a.join(''));
  const lengths = literals.nums.filter((n) => Number.isInteger(n) && n >= 0 && n <= 64);
  const sized = lengths.length
    ? fc.constantFrom(...new Set(lengths.flatMap((n) => [n - 1, n, n + 1]).filter((n) => n >= 0)))
        .chain((n) => fc.string({ minLength: n, maxLength: n }))
    : fc.string({ maxLength: 12 });
  // v2: the exact literal gets its own weight: it is exactly what the code compares against.
  return fc.oneof(
    { weight: 3, arbitrary: frag },
    { weight: 3, arbitrary: variant },
    { weight: 3, arbitrary: soup },
    { weight: 2, arbitrary: fc.string({ maxLength: 20 }) },
    { weight: 1, arbitrary: sized },
    { weight: 1, arbitrary: fc.string({ minLength: 10, maxLength: 48 }) },
  );
}

function numberArb(literals: { nums: number[] }): fc.Arbitrary<number> {
  const near = new Set<number>([0, 1, -1]);
  for (const n of literals.nums) for (const v of [n, n - 1, n + 1, -n, n / 100, n / 1000, n / 100 + 0.001, n / 100 - 0.001]) near.add(v);
  return fc.oneof(
    { weight: 3, arbitrary: fc.constantFrom(...near) },
    { weight: 2, arbitrary: fc.integer({ min: -1000, max: 1000 }) },
    { weight: 2, arbitrary: fc.integer({ min: 0, max: 1000 }).map((n) => n / 1000) },
    { weight: 1, arbitrary: fc.double({ noNaN: true, noDefaultInfinity: true, min: -1e6, max: 1e6 }) },
  );
}

class NoDerivable extends Error {}

function arbOf(t: ts.Type, ch: ts.TypeChecker, literals: ReturnType<typeof harvest>, depth: number, typed: { ok: boolean }): fc.Arbitrary<unknown> {
  if (depth > 4) throw new NoDerivable(`type too deep: ${ch.typeToString(t)}`);
  const f = t.flags;
  if (f & (ts.TypeFlags.Any | ts.TypeFlags.Unknown | ts.TypeFlags.TypeParameter)) {
    typed.ok = false;
    return fc.anything({ maxDepth: 2 });
  }
  if (f & ts.TypeFlags.StringLiteral) return fc.constant((t as ts.StringLiteralType).value);
  if (f & ts.TypeFlags.NumberLiteral) return fc.constant((t as ts.NumberLiteralType).value);
  if (f & ts.TypeFlags.BooleanLiteral) return fc.constant((t as unknown as { intrinsicName: string }).intrinsicName === 'true');
  if (f & ts.TypeFlags.String) return stringArb(literals);
  if (f & ts.TypeFlags.Number) return numberArb(literals);
  if (f & ts.TypeFlags.Boolean) return fc.boolean();
  if (f & ts.TypeFlags.BigInt) return fc.bigInt();
  if (f & (ts.TypeFlags.Undefined | ts.TypeFlags.Void)) return fc.constant(undefined);
  if (f & ts.TypeFlags.Null) return fc.constant(null);
  if (t.isUnion()) return fc.oneof(...t.types.map((x) => arbOf(x, ch, literals, depth + 1, typed)));
  if (f & ts.TypeFlags.Object || t.isIntersection()) {
    if (ch.isTupleType(t)) {
      return fc.tuple(...ch.getTypeArguments(t as ts.TypeReference).map((x) => arbOf(x, ch, literals, depth + 1, typed)));
    }
    if (ch.isArrayType(t)) {
      return fc.array(arbOf(ch.getTypeArguments(t as ts.TypeReference)[0], ch, literals, depth + 1, typed), { maxLength: 5 });
    }
    const name = t.getSymbol()?.getName();
    if (name === 'Date') return fc.date();
    if (t.getCallSignatures().length) throw new NoDerivable(`function parameter: ${ch.typeToString(t)}`);
    if (name && ['Map', 'Set', 'Promise', 'RegExp', 'WeakMap'].includes(name)) throw new NoDerivable(`type not derivable: ${name}`);
    const idx = ch.getIndexInfoOfType(t, ts.IndexKind.String);
    const props = ch.getPropertiesOfType(t);
    if (idx && props.length === 0) {
      return fc.dictionary(stringArb(literals), arbOf(idx.type, ch, literals, depth + 1, typed), { maxKeys: 4 });
    }
    const rec: Record<string, fc.Arbitrary<unknown>> = {};
    const required: string[] = [];
    for (const p of props) {
      const decl = p.valueDeclaration ?? p.declarations?.[0];
      if (!decl) continue;
      rec[p.getName()] = arbOf(ch.getTypeOfSymbolAtLocation(p, decl), ch, literals, depth + 1, typed);
      if (!(p.flags & ts.SymbolFlags.Optional)) required.push(p.getName());
    }
    return fc.record(rec, { requiredKeys: required });
  }
  throw new NoDerivable(`type not derivable: ${ch.typeToString(t)}`);
}

function argsArb(name: string, sf: ts.SourceFile, ch: ts.TypeChecker, literals: ReturnType<typeof harvest>, typed: { ok: boolean }) {
  const sym = ch.getSymbolAtLocation(sf)?.exports?.get(ts.escapeLeadingUnderscores(name));
  if (!sym) throw new NoDerivable('no exported symbol');
  const target = sym.flags & ts.SymbolFlags.Alias ? ch.getAliasedSymbol(sym) : sym;
  const decl = target.valueDeclaration ?? target.declarations?.[0];
  if (!decl) throw new NoDerivable('no declaration');
  const sig = ch.getTypeOfSymbolAtLocation(target, decl).getCallSignatures()[0];
  if (!sig) throw new NoDerivable('not callable');
  const params = sig.getParameters().map((p) => {
    const pd = p.valueDeclaration as ts.ParameterDeclaration | undefined;
    if (pd?.dotDotDotToken) throw new NoDerivable('rest parameter');
    let a = arbOf(ch.getTypeOfSymbolAtLocation(p, pd ?? decl), ch, literals, 0, typed);
    if (pd && (pd.questionToken || pd.initializer)) a = fc.option(a, { nil: undefined });
    return a;
  });
  return fc.tuple(...params);
}

// ---------- comparison ----------

// A promise (or async iterable) result is awaited, so a function is judged by what it resolves to. What a call must not
// do for real is replaced by traps while a target runs (network, filesystem writes, timers): the first one that fires
// makes the target INCONCLUSIVE with its name, since what the code does there cannot be compared between two runs. A call
// that does not settle within STEP_TIMEOUT_MS is not a result either (same rule as the Python engine's asyncdrive).
type Result = { ok: unknown } | { lanza: string } | { suspended: string } | { effect: string } | { opaque: string };

const STEP_TIMEOUT_MS = 1000;
const MAX_ITEMS = 1000; // an async iterable that yields more than this is treated as unbounded
const realSetTimeout = globalThis.setTimeout;
const realClearTimeout = globalThis.clearTimeout;
const FS_WRITES = ['writeFile', 'appendFile', 'mkdir', 'mkdtemp', 'rm', 'rmdir', 'unlink', 'rename', 'copyFile', 'cp', 'truncate',
  'symlink', 'link', 'write', 'createWriteStream'];

class Unbounded extends Error {}
class SideEffect extends Error {}

let effect = ''; // the first side effect a trap saw in the current target; empty if none
let sawAsync = false; // a call of the current target returned a promise or an async iterable
const trap = (label: string, real?: (...a: unknown[]) => unknown, self?: unknown) => (...a: unknown[]) => {
  // tsx caches what it transforms under <tmpdir>/tsx-<uid>: that is the engine's own loader, not the code under test.
  if (real && typeof a[0] === 'string' && path.resolve(a[0]).startsWith(path.join(tmpdir(), 'tsx-'))) return real.apply(self, a);
  effect ||= label;
  throw new SideEffect(label);
};
const openWrites = (...a: unknown[]) => {
  const flags = a[1];
  if (typeof flags === 'string' ? /[wa+]/.test(flags) : typeof flags === 'number' && (flags & 3) !== 0) throw new SideEffect('filesystem write (open)');
  return undefined;
};

type Holder = Record<string, unknown>;
function trapList(): [Holder, string, string][] {
  const out: [Holder, string, string][] = [
    [globalThis as unknown as Holder, 'fetch', 'network (fetch)'],
    [net.Socket.prototype as unknown as Holder, 'connect', 'network (socket connect)'],
    [net.Server.prototype as unknown as Holder, 'listen', 'network (server listen)'],
    [globalThis as unknown as Holder, 'setTimeout', 'timers (setTimeout)'],
    [globalThis as unknown as Holder, 'setInterval', 'timers (setInterval)'],
    [timers as unknown as Holder, 'setTimeout', 'timers (setTimeout)'],
    [timers as unknown as Holder, 'setInterval', 'timers (setInterval)'],
    [timersPromises as unknown as Holder, 'setTimeout', 'timers (setTimeout)'],
    [timersPromises as unknown as Holder, 'setInterval', 'timers (setInterval)'],
  ];
  for (const n of FS_WRITES) {
    out.push([fs as unknown as Holder, n, `filesystem write (${n})`], [fs as unknown as Holder, `${n}Sync`, `filesystem write (${n}Sync)`]);
    out.push([fs.promises as unknown as Holder, n, `filesystem write (${n})`]);
  }
  return out;
}

// Runs body with the traps installed and restores every original after, so the engine's own writes (response.json) are not
// trapped. syncBuiltinESMExports makes `import { writeFileSync } from 'node:fs'` in the loaded modules see the traps.
async function sandboxed<T>(body: () => Promise<T>): Promise<T> {
  const saved: [Holder, string, unknown][] = [];
  for (const [holder, key, label] of trapList()) {
    if (typeof holder[key] !== 'function') continue;
    saved.push([holder, key, holder[key]]);
    holder[key] = trap(label, label.startsWith('filesystem') ? holder[key] as (...a: unknown[]) => unknown : undefined, holder);
  }
  for (const k of ['open', 'openSync']) {
    saved.push([fs as unknown as Holder, k, (fs as unknown as Holder)[k]]);
    const real = (fs as unknown as Holder)[k] as (...a: unknown[]) => unknown;
    (fs as unknown as Holder)[k] = (...a: unknown[]) => {
      try { openWrites(...a); } catch (e) { effect ||= (e as Error).message; throw e; }
      return real(...a);
    };
  }
  syncBuiltinESMExports();
  try {
    return await body();
  } finally {
    for (const [holder, key, orig] of saved.reverse()) holder[key] = orig;
    syncBuiltinESMExports();
  }
}

const LATE = Symbol('late');
const settle = <T>(p: PromiseLike<T>) => new Promise<T | typeof LATE>((res, rej) => {
  const t = realSetTimeout(() => res(LATE), STEP_TIMEOUT_MS);
  p.then((v) => { realClearTimeout(t); res(v); }, (e) => { realClearTimeout(t); rej(e); });
});

async function collect(it: AsyncIterable<unknown>): Promise<unknown[]> {
  const items: unknown[] = [];
  for await (const x of it) {
    items.push(x);
    if (items.length > MAX_ITEMS) throw new Unbounded();
  }
  return items;
}

// What in a result is not data to compare: a function (isDeepStrictEqual compares it by reference, so two equal versions
// would differ), or a server or socket (a network resource: the call opened one).
function opaqueOf(v: unknown, depth = 0, seen = new Set<object>()): string | null {
  if (typeof v === 'function') return 'function';
  if (v === null || typeof v !== 'object' || depth > 4 || seen.has(v)) return null;
  seen.add(v);
  if (v instanceof net.Server || v instanceof net.Socket) return 'network';
  const inner = v instanceof Map ? [...v.keys(), ...v.values()] : v instanceof Set ? [...v] : Object.values(v);
  for (const x of inner) {
    const o = opaqueOf(x, depth + 1, seen);
    if (o) return o;
  }
  return null;
}

const drive = async (fn: (...a: unknown[]) => unknown, a: unknown[]): Promise<Result> => {
  try {
    let r = fn(...structuredClone(a));
    if (r && typeof (r as { [Symbol.asyncIterator]?: unknown })[Symbol.asyncIterator] === 'function') r = collect(r as AsyncIterable<unknown>);
    if (r && typeof (r as { then?: unknown }).then === 'function') {
      sawAsync = true;
      const v = await settle(r as PromiseLike<unknown>);
      if (v === LATE) return { suspended: 'did not settle' };
      r = v;
    }
    if (effect) return { effect };
    const o = opaqueOf(r);
    return o ? { opaque: o } : { ok: r };
  } catch (e) {
    if (effect) return { effect };
    if (e instanceof Unbounded) return { suspended: `yielded more than ${MAX_ITEMS} items` };
    return { lanza: `${(e as Error)?.constructor?.name}: ${(e as Error)?.message}` };
  }
};
const text = (v: unknown) => (JSON.stringify(v, (_k, x) => (x === undefined ? '<undefined>' : typeof x === 'bigint' ? `${x}n` : x)) ?? 'undefined').slice(0, 300);

// Compares the two versions of one target and writes the verdict into t. A FAIL needs a counterexample from calls that both
// returned or both raised. A call that touched the network, the filesystem or a timer, returned a server or a function, or
// did not settle is no evidence for a PASS, so the target is INCONCLUSIVE with that reason (a FAIL found elsewhere stays FAIL).
async function judge(
  t: Json, old: (...a: unknown[]) => unknown, fresh: (...a: unknown[]) => unknown,
  args: fc.Arbitrary<unknown[]>, inconclusive: (reason: string) => unknown,
): Promise<void> {
  const seen = { old: { ok: 0, raised: 0 }, new: { ok: 0, raised: 0 } };
  const unjudged = { suspended: 0, opaque: '', stray: '' };
  sawAsync = false;
  effect = '';
  const onStray = (e: unknown) => { unjudged.stray ||= `${(e as Error)?.message ?? e}`.slice(0, 120); };
  process.on('unhandledRejection', onStray);
  process.on('uncaughtException', onStray);
  try {
    const r = await sandboxed(() => fc.check(
      fc.asyncProperty(args, async (a) => {
        if (effect || unjudged.opaque || unjudged.stray || unjudged.suspended) return true; // already not judgeable: stop spending the budget
        const x = await drive(old, a), y = await drive(fresh, a);
        for (const [side, res] of [['old', x], ['new', y]] as const) {
          if ('ok' in res) seen[side].ok++;
          else if ('lanza' in res) seen[side].raised++;
          else if ('suspended' in res) unjudged.suspended++;
          else if ('opaque' in res) unjudged.opaque ||= res.opaque;
        }
        if (effect || unjudged.opaque || unjudged.stray || unjudged.suspended) return true;
        if (!('ok' in x || 'lanza' in x) || !('ok' in y || 'lanza' in y)) return true;
        return isDeepStrictEqual(x, y);
      }),
      { numRuns: EXAMPLES, seed: SEED },
    ));
    if (r.failed && r.counterexample) {
      const ce = r.counterexample[0] as unknown[];
      const hit = effect;
      effect = '';
      const [x, y] = await sandboxed(async () => [await drive(old, ce), await drive(fresh, ce)] as const);
      effect = hit;
      Object.assign(t, { verdict: 'FAIL', counterexample: { args: text(ce), old: text(x), new: text(y) } });
    } else if (r.failed) {
      inconclusive(`fast-check failed without a counterexample: ${String(r.errorInstance ?? '').slice(0, 150)}`);
    } else if (effect) {
      inconclusive(`the function does ${effect}, which cannot be sandboxed: it was not judged`);
    } else if (unjudged.opaque) {
      inconclusive(unjudged.opaque === 'network'
        ? 'the function returns a server or a socket (network, which cannot be sandboxed): its result is not comparable data'
        : 'the result contains a function: it is not comparable data');
    } else if (unjudged.stray) {
      inconclusive(`an error escaped the call (${unjudged.stray}): the function was not judged`);
    } else if (unjudged.suspended) {
      inconclusive(`${unjudged.suspended} call(s) did not finish within ${STEP_TIMEOUT_MS / 1000} s (real I/O, a long wait, a promise nobody settles): those paths were not judged`);
    } else if (sawAsync && (!seen.old.ok || !seen.new.ok)) {
      inconclusive(`no call of the ${seen.old.ok ? 'new' : 'old'} version returned a value (every one raised): nothing was compared`);
    }
  } finally {
    process.off('unhandledRejection', onStray);
    process.off('uncaughtException', onStray);
  }
}

// ---------- main ----------

const targets: Json[] = [];
const outOfScope: Json[] = [];
let override: Record<string, fc.Arbitrary<unknown[]>> = {};
let overrideError = '';
if (req.generators_override && existsSync(`${req.candidate_dir}/${req.generators_override}`)) {
  try {
    const m = await import(pathToFileURL(`${req.candidate_dir}/${req.generators_override}`).href);
    override = (m.default as (x: typeof fc) => Record<string, fc.Arbitrary<unknown[]>>)(fc);
  } catch (e) {
    overrideError = `override ${req.generators_override}: ${(e as Error).message}`.slice(0, 200);
  }
}

for (const file of req.changed_files as string[]) {
  if (!/\.(ts|tsx|mts|js|mjs)$/.test(file)) {
    outOfScope.push({ file, reason: 'not_exported' });
    continue;
  }
  const basePath = `${req.base_dir}/${file}`;
  const candPath = `${req.candidate_dir}/${file}`;
  if (!existsSync(basePath)) {
    outOfScope.push({ file, reason: 'added' });
    continue;
  }
  const program = ts.createProgram([candPath], { strict: true, allowJs: true, noEmit: true, skipLibCheck: true, target: ts.ScriptTarget.ES2022 });
  const ch = program.getTypeChecker();
  const candSf = program.getSourceFile(candPath)!;
  const baseSf = ts.createSourceFile(basePath, readFileSync(basePath, 'utf-8'), ts.ScriptTarget.ES2022, true);
  const names = targetsOf(baseSf, candSf);
  const same = equivalentOf(baseSf, candSf);
  for (const d of same) {
    targets.push({
      target: `${file}::${d.name}`, engine: 'fast-check', engine_version: FC_VERSION, verdict: 'PASS', reason: EQUIVALENT_AST,
      search_budget: { examples: EXAMPLES, seed: SEED }, generators: 'typed_default', typed: true, domain_guard: null,
      equivalence: 'strict', counterexample: null, lines: d.lines,
    });
  }
  if (!names.length) {
    if (!same.length) outOfScope.push({ file, reason: 'not_exported' });
    continue;
  }
  const literals = harvest([baseSf, candSf]);
  let oldMod: Json | undefined, newMod: Json | undefined, importErr = '';
  effect = '';
  try {
    // Loading a module can itself open a socket or arm a timer: the traps are on here too, and say so.
    await sandboxed(async () => {
      oldMod = await import(pathToFileURL(basePath).href);
      newMod = await import(pathToFileURL(candPath).href);
    });
  } catch (e) {
    importErr = (effect ? `could not import: loading the module does ${effect}, which cannot be sandboxed` : `could not import: ${(e as Error).message}`).slice(0, 200);
  }
  for (const name of names) {
    const ov = override[name] ?? override[`${file}::${name}`];
    const typed = { ok: true };
    const t: Json = {
      target: `${file}::${name}`, engine: 'fast-check', engine_version: FC_VERSION, verdict: 'PASS',
      search_budget: { examples: EXAMPLES, seed: SEED }, generators: ov ? 'repo_override' : 'typed_default',
      typed: true, domain_guard: null, equivalence: 'strict', counterexample: null,
    };
    targets.push(t);
    const inconclusive = (reason: string) => Object.assign(t, { verdict: 'INCONCLUSIVE', reason });
    if (overrideError && !ov) inconclusive(overrideError);
    if (importErr) { inconclusive(importErr); continue; }
    const old = oldMod![name] as (...a: unknown[]) => unknown;
    const fresh = newMod![name] as (...a: unknown[]) => unknown;
    if (typeof old !== 'function' || typeof fresh !== 'function') { inconclusive('the export is not a function in one of the versions'); continue; }
    let args: fc.Arbitrary<unknown[]>;
    try {
      args = ov ?? argsArb(name, candSf, ch, literals, typed);
    } catch (e) {
      inconclusive((e as Error).message.slice(0, 200));
      t.typed = false;
      continue;
    }
    t.typed = typed.ok;
    // One target never drops the file: whatever its judgment throws is that target's INCONCLUSIVE.
    try {
      await judge(t, old, fresh, args, inconclusive);
    } catch (e) {
      inconclusive(`the engine could not judge this function: ${(e as Error).message}`.slice(0, 200));
    }
  }
}

writeFileSync(`${IO}/response.json`, JSON.stringify({ schema_version: 1, targets, out_of_scope: outOfScope, error: null }, null, 1));
// Promises that never settle or handles a target left open must not keep the container alive until the core's timeout.
process.exit(0);
