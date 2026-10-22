// Stubs for the NOP gate (protocol §5, Stage 4): the base receives a stub of each symbol the candidate added, so that a
// new test fails because of the test ("a2dlc stub"), not because of a missing export. The base's own bodies and values
// are not touched: they may be the fix itself. Stubs carry no type annotations and no parameter defaults (they may name
// things that only exist in the candidate), and every stub is a plain function, class or value, so the file stays
// valid JavaScript.
import ts from 'typescript';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { TARGET_FILE, parse, parses } from './tier0.ts';

export interface StubRequest { base_dir: string; candidate_dir: string; changed_files: string[] }
export interface StubResult { text: string | null; symbols: string[]; unstubbed: string[] }

const ARGS = '...__a2dlc';
const THROW = 'throw new Error("a2dlc stub");';
const HEADER = '// --- a2dlc stubs (NOP gate) ---';

type ExportKind = 'function' | 'class' | 'value' | 'reexport';
interface ExportInfo { kind: ExportKind; async: boolean; decl?: ts.Node; name: string }

const has = (n: ts.Node, kind: ts.SyntaxKind) => ts.canHaveModifiers(n) && (ts.getModifiers(n) ?? []).some((m) => m.kind === kind);
const isAsync = (n: ts.Node) => has(n, ts.SyntaxKind.AsyncKeyword);
const isExported = (n: ts.Node) => has(n, ts.SyntaxKind.ExportKeyword);
const isDefault = (n: ts.Node) => has(n, ts.SyntaxKind.DefaultKeyword);

// The top level of a module: what it exports (inline, by list and by default), what it binds, and its classes.
interface Surface {
  exports: Map<string, ExportInfo>;
  bound: Set<string>;
  classes: Map<string, ts.ClassDeclaration>;
  // Module specifiers of `export * from "x"`: their names are not listed, so they cannot be stubbed.
  stars: string[];
}

function surfaceOf(sf: ts.SourceFile): Surface {
  const s: Surface = { exports: new Map(), bound: new Set(), classes: new Map(), stars: [] };
  const locals = new Map<string, ExportInfo>();
  const listed: Array<{ exported: string; local: string }> = [];
  const addDecl = (name: string, info: ExportInfo, exported: boolean) => {
    s.bound.add(name);
    locals.set(name, info);
    if (exported) s.exports.set(name, info);
  };
  for (const st of sf.statements) {
    if (ts.isFunctionDeclaration(st)) {
      const name = st.name?.text ?? 'default';
      const info: ExportInfo = { kind: 'function', async: isAsync(st), decl: st, name };
      if (st.name) addDecl(name, info, isExported(st) && !isDefault(st));
      if (isExported(st) && isDefault(st)) s.exports.set('default', info);
    } else if (ts.isClassDeclaration(st)) {
      const name = st.name?.text ?? 'default';
      if (st.name) s.classes.set(name, st);
      const info: ExportInfo = { kind: 'class', async: false, decl: st, name };
      if (st.name) addDecl(name, info, isExported(st) && !isDefault(st));
      if (isExported(st) && isDefault(st)) s.exports.set('default', info);
    } else if (ts.isEnumDeclaration(st)) {
      addDecl(st.name.text, { kind: 'value', async: false, name: st.name.text }, isExported(st));
    } else if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) {
        const names = bindingNames(d.name);
        for (const name of names) {
          const init = d.initializer && unparenthesize(d.initializer);
          const fn = init && (ts.isArrowFunction(init) || ts.isFunctionExpression(init)) ? init : undefined;
          const info: ExportInfo = fn
            ? { kind: 'function', async: isAsync(fn), decl: d, name }
            : { kind: 'value', async: false, decl: d, name };
          addDecl(name, info, isExported(st));
        }
      }
    } else if (ts.isExportAssignment(st) && !st.isExportEquals) {
      s.exports.set('default', { kind: 'value', async: false, name: 'default' });
    } else if (ts.isExportDeclaration(st)) {
      if (st.moduleSpecifier) {
        const spec = ts.isStringLiteral(st.moduleSpecifier) ? st.moduleSpecifier.text : '';
        if (!st.exportClause) {
          s.stars.push(spec);
        } else if (ts.isNamespaceExport(st.exportClause)) {
          const name = st.exportClause.name.text;
          s.exports.set(name, { kind: 'reexport', async: false, name });
        } else {
          for (const el of st.exportClause.elements) {
            const name = el.name.text;
            s.exports.set(name, { kind: 'reexport', async: false, name });
          }
        }
      } else if (st.exportClause && ts.isNamedExports(st.exportClause)) {
        for (const el of st.exportClause.elements) listed.push({ exported: el.name.text, local: (el.propertyName ?? el.name).text });
      }
    }
  }
  for (const { exported, local } of listed) {
    const info = locals.get(local);
    if (info) s.exports.set(exported, { ...info, name: exported });
    else s.exports.set(exported, { kind: 'value', async: false, name: exported });
  }
  return s;
}

function unparenthesize(e: ts.Expression): ts.Expression {
  let x = e;
  while (ts.isParenthesizedExpression(x)) x = x.expression;
  return x;
}

function bindingNames(name: ts.BindingName): string[] {
  if (ts.isIdentifier(name)) return [name.text];
  const out: string[] = [];
  for (const el of name.elements) {
    if (ts.isOmittedExpression(el)) continue;
    out.push(...bindingNames(el.name));
  }
  return out;
}

function memberKey(name: ts.PropertyName): string | null {
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name)) return name.text;
  if (ts.isPrivateIdentifier(name)) return name.text;
  return null;
}

// Bodies of a class: each method, accessor and the constructor throw; the heritage clause and the property
// initializers are not carried over (they may name what only the candidate has).
function classStub(head: string, label: string, decl: ts.ClassDeclaration, sf: ts.SourceFile, unstubbed: Set<string>): string {
  const members: string[] = [`constructor(${ARGS}) { ${THROW} }`];
  for (const m of decl.members) {
    if (ts.isConstructorDeclaration(m) || !m.name) continue;
    const key = memberKey(m.name);
    const isStatic = has(m, ts.SyntaxKind.StaticKeyword) ? 'static ' : '';
    const text = m.name.getText(sf);
    if (key === null) {
      if (ts.isMethodDeclaration(m) || ts.isGetAccessorDeclaration(m) || ts.isSetAccessorDeclaration(m)) unstubbed.add(`${label}.${text}`);
      continue;
    }
    if (ts.isMethodDeclaration(m) && m.body) {
      members.push(`${isStatic}${isAsync(m) ? 'async ' : ''}${text}(${ARGS}) { ${THROW} }`);
    } else if ((ts.isGetAccessorDeclaration(m) || ts.isSetAccessorDeclaration(m)) && m.body) {
      const getter = ts.isGetAccessorDeclaration(m);
      members.push(`${isStatic}${getter ? 'get' : 'set'} ${text}(${getter ? '' : ARGS}) { ${THROW} }`);
    }
  }
  return `${head} {\n  ${members.join('\n  ')}\n}`;
}

// The stub of one new export, or null when it has no runtime form to stub (a re-export is reported as unstubbed).
function exportStub(name: string, info: ExportInfo, sf: ts.SourceFile, unstubbed: Set<string>): string | null {
  const a = info.async ? 'async ' : '';
  if (info.kind === 'reexport') {
    unstubbed.add(name);
    return null;
  }
  if (info.kind === 'class' && info.decl && ts.isClassDeclaration(info.decl)) {
    const head = name === 'default' ? 'export default class' : `export class ${name}`;
    return classStub(head, name, info.decl, sf, unstubbed);
  }
  if (info.kind === 'function') {
    if (name === 'default') return `export default ${a}function (${ARGS}) { ${THROW} }`;
    if (info.decl && ts.isFunctionDeclaration(info.decl)) return `export ${a}function ${name}(${ARGS}) { ${THROW} }`;
    return `export const ${name} = ${a}(${ARGS}) => { ${THROW} };`;
  }
  return name === 'default' ? 'export default undefined;' : `export let ${name} = undefined;`;
}

// The stub of a module: `base` null means the module is new (every export is new). Returns null text when nothing new
// has a stub; symbols and unstubbed name what the engine could and could not stub.
export function stubOf(baseText: string | null, candText: string, file: string): StubResult {
  const cand = parse(candText, file);
  const candSurface = surfaceOf(cand);
  const baseSf = baseText === null ? undefined : parse(baseText, file);
  const base = baseSf ? surfaceOf(baseSf) : null;
  const unstubbed = new Set<string>();
  const symbols: string[] = [];
  const out: string[] = [];

  // `export * from` adds names the engine cannot list: a new star re-export is reported, by its specifier.
  for (const spec of candSurface.stars) {
    if (!base || !base.stars.includes(spec)) unstubbed.add(`* from ${spec}`);
  }

  for (const [name, info] of candSurface.exports) {
    if (base && base.exports.has(name)) continue;
    if (info.kind === 'reexport') { unstubbed.add(name); continue; }
    if (base && base.bound.has(name)) { unstubbed.add(name); continue; }
    const stub = exportStub(name, info, cand, unstubbed);
    if (stub === null) continue;
    out.push(stub);
    symbols.push(name);
  }

  // New methods of a top-level class that the base already has: patched onto the class, as Python does.
  if (base && baseSf) {
    for (const [cname, cdecl] of candSurface.classes) {
      const bclass = base.classes.get(cname);
      if (!bclass) continue;
      const existing = new Set(bclass.members.map((m) => `${has(m, ts.SyntaxKind.StaticKeyword) ? 'static ' : ''}${m.name ? m.name.getText(baseSf) : ''}`));
      for (const m of cdecl.members) {
        const isAccessor = ts.isGetAccessorDeclaration(m) || ts.isSetAccessorDeclaration(m);
        if (isAccessor || ts.isMethodDeclaration(m)) {
          if (!m.body || !m.name) continue;
          const label = `${cname}.${m.name.getText(cand)}`;
          const key = memberKey(m.name);
          if (key === null || isAccessor) { unstubbed.add(label); continue; }
          const isStatic = has(m, ts.SyntaxKind.StaticKeyword);
          if (existing.has(`${isStatic ? 'static ' : ''}${m.name.getText(cand)}`)) continue;
          const target = isStatic ? cname : `${cname}.prototype`;
          out.push(`${target}[${JSON.stringify(key)}] = ${isAsync(m) ? 'async ' : ''}function (${ARGS}) { ${THROW} };`);
          symbols.push(`${cname}.${key}`);
        }
      }
    }
  }

  if (!out.length) return { text: null, symbols: [], unstubbed: [...unstubbed].sort() };
  const prefix = base ? `${baseText!.trimEnd()}\n\n` : '';
  return { text: `${prefix}${HEADER}\n${out.join('\n')}\n`, symbols, unstubbed: [...unstubbed].sort() };
}

// Writes /io/stubs/<file> for each changed file and /io/response.json (contracts/engine-stub-response.schema.json).
export function main(req: StubRequest, io: string): void {
  const stubbed: Array<{ file: string; symbols: string[] }> = [];
  const unstubbed = new Set<string>();
  let body: Record<string, unknown>;
  try {
    for (const file of req.changed_files) {
      if (!TARGET_FILE.test(file)) continue;
      const candPath = path.join(req.candidate_dir, file);
      if (!existsSync(candPath)) continue;
      const candText = readFileSync(candPath, 'utf-8');
      const basePath = path.join(req.base_dir, file);
      const baseText = existsSync(basePath) ? readFileSync(basePath, 'utf-8') : null;
      // An unparseable file has no surface to stub: the skip is reported, so the NOP gate sees why nothing was stubbed.
      if (!parses(candText, file) || (baseText !== null && !parses(baseText, file))) {
        unstubbed.add(`unparseable: ${file}`);
        continue;
      }
      const r = stubOf(baseText, candText, file);
      for (const u of r.unstubbed) unstubbed.add(u);
      if (r.text === null) continue;
      const dest = path.join(io, 'stubs', file);
      mkdirSync(path.dirname(dest), { recursive: true });
      writeFileSync(dest, r.text, 'utf-8');
      stubbed.push({ file, symbols: r.symbols });
    }
    body = { schema_version: 1, stubbed, unstubbed: [...unstubbed].sort(), error: null };
  } catch (e) {
    body = { schema_version: 1, stubbed: [], unstubbed: [], error: `stub crashed: ${(e as Error).message ?? String(e)}`.slice(0, 300) };
  }
  writeFileSync(`${io}/response.json`, JSON.stringify(body, null, 1));
}
