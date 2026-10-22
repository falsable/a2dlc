// a2dlc TypeScript engine: differential check with fast-check over the exported functions that changed.
// Contract: reads /io/request.json and writes /io/response.json (contracts/engine-*.schema.json).
//
// Targets: exported functions whose text changed, plus those that use (within the file, transitively) a top-level
// declaration that changed. So a change in a helper that is not isExported, or in a constant, counts.
// Generators (typed_default): derived from the parameter types, plus the literals of both versions of the file
// and their boundaries (n±1, n/100, strings of length n). Override: .a2dlc/arbs.ts with
// `export default (fc) => ({ "<function>": fc.tuple(...) })`, without importing fast-check (the repo does not have it).
import fc from 'fast-check';
import ts from 'typescript';
import { isDeepStrictEqual } from 'node:util';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';

type Json = Record<string, unknown>;
const IO = process.env.A2DLC_IO ?? '/io';
const req = JSON.parse(readFileSync(`${IO}/request.json`, 'utf-8'));
const budget = req.budget ?? {};
const EXAMPLES: number = budget.examples ?? 2000;
const SEED: number = budget.seed ?? 42;
const FC_VERSION: string = createRequire(import.meta.url)('fast-check/package.json').version;

// ---------- target discovery ----------

interface Decl {
  name: string;
  text: string;
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
      out.set(name, { name, text: node.getText(sf), exported: isExported(node), isFunction, refs: refsOf(node) });
    if (ts.isFunctionDeclaration(st) && st.name) add(st.name.text, st, true);
    else if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) {
        if (!ts.isIdentifier(d.name)) continue;
        const init = d.initializer;
        const fn = !!init && (ts.isArrowFunction(init) || ts.isFunctionExpression(init));
        out.set(d.name.text, { name: d.name.text, text: st.getText(sf), exported: isExported(st), isFunction: fn, refs: refsOf(d) });
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

type Result = { ok: unknown } | { lanza: string } | { async: true };
const run = (fn: (...a: unknown[]) => unknown, a: unknown[]): Result => {
  try {
    const r = fn(...structuredClone(a));
    if (r && typeof (r as { then?: unknown }).then === 'function') return { async: true };
    return { ok: r };
  } catch (e) {
    return { lanza: `${(e as Error)?.constructor?.name}: ${(e as Error)?.message}` };
  }
};
const text = (v: unknown) => (JSON.stringify(v, (_k, x) => (x === undefined ? '<undefined>' : typeof x === 'bigint' ? `${x}n` : x)) ?? 'undefined').slice(0, 300);

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
  if (!names.length) {
    outOfScope.push({ file, reason: 'not_exported' });
    continue;
  }
  const literals = harvest([baseSf, candSf]);
  let oldMod: Json | undefined, newMod: Json | undefined, importErr = '';
  try {
    oldMod = await import(pathToFileURL(basePath).href);
    newMod = await import(pathToFileURL(candPath).href);
  } catch (e) {
    importErr = `could not import: ${(e as Error).message}`.slice(0, 200);
  }
  for (const name of names) {
    const override = override[name] ?? override[`${file}::${name}`];
    const typed = { ok: true };
    const t: Json = {
      target: `${file}::${name}`, engine: 'fast-check', engine_version: FC_VERSION, verdict: 'PASS',
      search_budget: { examples: EXAMPLES, seed: SEED }, generators: override ? 'repo_override' : 'typed_default',
      typed: true, domain_guard: null, equivalence: 'strict', counterexample: null,
    };
    targets.push(t);
    const inconclusive = (reason: string) => Object.assign(t, { verdict: 'INCONCLUSIVE', reason });
    if (overrideError && !override) inconclusive(overrideError);
    if (importErr) { inconclusive(importErr); continue; }
    const old = oldMod![name] as (...a: unknown[]) => unknown;
    const fresh = newMod![name] as (...a: unknown[]) => unknown;
    if (typeof old !== 'function' || typeof fresh !== 'function') { inconclusive('the export is not a function in one of the versions'); continue; }
    let args: fc.Arbitrary<unknown[]>;
    try {
      args = override ?? argsArb(name, candSf, ch, literals, typed);
    } catch (e) {
      inconclusive((e as Error).message.slice(0, 200));
      t.typed = false;
      continue;
    }
    t.typed = typed.ok;
    let isAsync = false;
    const r = fc.check(
      fc.property(args, (a) => {
        const x = run(old, a), y = run(fresh, a);
        if ('async' in x || 'async' in y) { isAsync = true; return true; }
        return isDeepStrictEqual(x, y);
      }),
      { numRuns: EXAMPLES, seed: SEED },
    );
    if (isAsync) { inconclusive('async function: not supported yet'); continue; }
    if (r.failed && r.counterexample) {
      const ce = r.counterexample[0] as unknown[];
      Object.assign(t, { verdict: 'FAIL', counterexample: { args: text(ce), old: text(run(old, ce)), new: text(run(fresh, ce)) } });
    } else if (r.failed) {
      inconclusive(`fast-check failed without a counterexample: ${String(r.errorInstance ?? '').slice(0, 150)}`);
    }
  }
}

writeFileSync(`${IO}/response.json`, JSON.stringify({ schema_version: 1, targets, out_of_scope: outOfScope, error: null }, null, 1));
