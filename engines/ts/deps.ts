// Import graph of the candidate (protocol §5, deps_graph; contracts/engine-deps-response.schema.json). The edges run from
// the importer to the imported file, both relative to the candidate root. Every import that does not map to a file of the
// candidate (a package, a node: builtin, a missing file, a non-literal specifier) is listed in unresolved: never dropped.
import ts from 'typescript';
import { existsSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { TARGET_FILE, parse, parses } from './tier0.ts';

export interface DepsRequest { candidate_dir: string; changed_files: string[] }
export interface DepsResult { edges: Array<{ from: string; to: string }>; unresolved: string[] }

const EXTENSIONS = ['.ts', '.tsx', '.mts', '.cts', '.js', '.jsx', '.mjs', '.cjs'];
// ESM code imports the emitted name: './a.js' names the source './a.ts', and so on.
const EMITTED: Record<string, string[]> = {
  '.js': ['.ts', '.tsx', '.js', '.jsx'],
  '.jsx': ['.tsx', '.jsx'],
  '.mjs': ['.mts', '.mjs'],
  '.cjs': ['.cts', '.cjs'],
};

function literal(e: ts.Expression | undefined): string | null {
  return e && (ts.isStringLiteral(e) || ts.isNoSubstitutionTemplateLiteral(e)) ? e.text : null;
}

// Every module specifier of a file: static imports and re-exports, import-equals, import() and require(). A dynamic
// one with a non-literal specifier cannot be resolved: it is returned as null, so the caller can report it.
export function specifiersOf(sf: ts.SourceFile): Array<string | null> {
  const out: Array<string | null> = [];
  const visit = (n: ts.Node): void => {
    if ((ts.isImportDeclaration(n) || ts.isExportDeclaration(n)) && n.moduleSpecifier) {
      out.push(literal(n.moduleSpecifier as ts.Expression));
    } else if (ts.isImportEqualsDeclaration(n) && ts.isExternalModuleReference(n.moduleReference)) {
      out.push(literal(n.moduleReference.expression));
    } else if (ts.isCallExpression(n)) {
      const callee = n.expression;
      const isImport = callee.kind === ts.SyntaxKind.ImportKeyword;
      const isRequire = ts.isIdentifier(callee) && callee.text === 'require';
      if (isImport || isRequire) out.push(literal(n.arguments[0]));
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

const isFile = (p: string) => existsSync(p) && statSync(p).isFile();

// The candidate file a relative specifier names, as a posix path from the candidate root, or null.
export function resolveRelative(candidateDir: string, fromFile: string, spec: string): string | null {
  if (!/^\.{1,2}(\/|$)/.test(spec)) return null;
  const target = path.posix.normalize(path.posix.join(path.posix.dirname(fromFile), spec));
  if (target === '..' || target.startsWith('../')) return null;
  const ext = path.posix.extname(target);
  const tries: string[] = [];
  if (EMITTED[ext]) {
    const stem = target.slice(0, -ext.length);
    for (const e of EMITTED[ext]) tries.push(stem + e);
  }
  tries.push(target);
  for (const e of EXTENSIONS) tries.push(target + e);
  for (const e of EXTENSIONS) tries.push(`${target}/index${e}`);
  return tries.find((rel) => isFile(path.join(candidateDir, rel))) ?? null;
}

export function depsOf(candidateDir: string, changed: string[]): DepsResult {
  const edges = new Map<string, { from: string; to: string }>();
  const unresolved = new Set<string>();
  for (const file of changed) {
    if (!TARGET_FILE.test(file) || !isFile(path.join(candidateDir, file))) continue;
    const text = readFileSync(path.join(candidateDir, file), 'utf-8');
    if (!parses(text, file)) unresolved.add(`unparseable: ${file}`);
    for (const spec of specifiersOf(parse(text, file))) {
      if (spec === null) {
        unresolved.add(`import or require with a non-literal specifier: ${file}`);
        continue;
      }
      const to = resolveRelative(candidateDir, file, spec);
      if (to === null) unresolved.add(spec);
      else edges.set(`${file}\u0000${to}`, { from: file, to });
    }
  }
  return { edges: [...edges.values()], unresolved: [...unresolved].sort() };
}

// Writes /io/response.json (contracts/engine-deps-response.schema.json). A crash is an error response with no edges.
export function main(req: DepsRequest, io: string): void {
  let body: Record<string, unknown>;
  try {
    const r = depsOf(req.candidate_dir, req.changed_files);
    body = { schema_version: 1, edges: r.edges, unresolved: r.unresolved, error: null };
  } catch (e) {
    body = { schema_version: 1, edges: [], unresolved: [], error: `deps_graph crashed: ${(e as Error).message ?? String(e)}`.slice(0, 300) };
  }
  writeFileSync(`${io}/response.json`, JSON.stringify(body, null, 1));
}
