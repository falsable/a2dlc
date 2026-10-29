// Operation classify_lines (coverage notes, docs/COVERAGE_NOTES.md): which of the changed lines that no engine target
// examined and no test executed only declare. Node's coverage over tsx source maps reports the lines the compiler erased
// (a type alias, an interface, a type-only import) with zero hits, so a changed type could never be "executed" and the
// verdict stayed INCONCLUSIVE because of it. The core hands over budget.lines (file -> unexamined lines of the candidate);
// the answer is the subset that is declaration-only. Every other line is left out and keeps blocking: this module only
// ever lowers what blocks, so each rule is as narrow as the case it was written for.
//
// declarative_only: a line with no executable token on it. That is every line of a type alias, an interface, a type-only
// import or export (`import type`, `export type`, or a specifier list where every specifier is `type`) and anything marked
// `declare`, which emit no JavaScript; and the blank and comment-only lines around them, because V8 over the source map
// also reports zero hits on the blank line that follows an erased declaration (measured on the Entropos candidate). A line
// that shares a token with executable code is not noted, so `type A = number; const a: A = 1;` is not, and neither is a
// blank line inside a template literal. Not classified: value imports (they run at load time), enums, namespaces with
// values, abstract classes, overload signatures, defensive `catch` blocks, and every other statement. Only TypeScript
// sources are read: the same text in a .js file is not a type.
import ts from 'typescript';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { lineOf, parse, parses } from './tier0.ts';

export interface LineNote { category: 'declarative_only'; lines: number[] }
export interface ClassifyRequest {
  candidate_dir: string;
  changed_files: string[];
  budget?: { lines?: Record<string, number[]> };
}

const TS_SOURCE = /\.(ts|tsx|mts|cts)$/;
const DECLARATIVE = 'declarative_only';

const isDeclare = (n: ts.Node) => ts.canHaveModifiers(n) && (ts.getModifiers(n) ?? []).some((m) => m.kind === ts.SyntaxKind.DeclareKeyword);

// A node the compiler erases whole.
function typeOnly(n: ts.Node): boolean {
  if (ts.isTypeAliasDeclaration(n) || ts.isInterfaceDeclaration(n)) return true;
  if (ts.isImportDeclaration(n)) {
    const clause = n.importClause;
    if (!clause) return false; // `import './x'` runs the module
    if (clause.isTypeOnly) return true;
    const b = clause.namedBindings;
    return !clause.name && b !== undefined && ts.isNamedImports(b) && b.elements.length > 0 && b.elements.every((e) => e.isTypeOnly);
  }
  if (ts.isImportEqualsDeclaration(n)) return n.isTypeOnly;
  if (ts.isExportDeclaration(n)) {
    const c = n.exportClause;
    return n.isTypeOnly || (c !== undefined && ts.isNamedExports(c) && c.elements.length > 0 && c.elements.every((e) => e.isTypeOnly));
  }
  return isDeclare(n);
}

const isJsDoc = (n: ts.Node) => n.kind >= ts.SyntaxKind.FirstJSDocNode && n.kind <= ts.SyntaxKind.LastJSDocNode;

// Notes of one source for the lines asked about. The walk collects the lines that hold a token of executable code and skips
// every type-only node whole, so the lines of an erased declaration, the blank lines and the comment-only lines are the ones
// left out of that set. A line asked about is noted when it is in the file and not in the set.
export function classify(text: string, file: string, lines: number[]): LineNote[] {
  if (!TS_SOURCE.test(file)) return [];
  const sf = parse(text, file);
  const codeLines = new Set<number>();
  const walk = (n: ts.Node) => {
    if (isJsDoc(n) || n.kind === ts.SyntaxKind.EndOfFileToken || typeOnly(n)) return;
    const children = n.getChildren(sf);
    if (children.length > 0) return children.forEach(walk);
    const start = n.getStart(sf);
    for (let l = lineOf(sf, start); l <= lineOf(sf, Math.max(start, n.getEnd() - 1)); l++) codeLines.add(l);
  };
  walk(sf);
  const last = lineOf(sf, sf.getEnd());
  const noted = [...new Set(lines)].filter((l) => l >= 1 && l <= last && !codeLines.has(l)).sort((a, b) => a - b);
  return noted.length > 0 ? [{ category: DECLARATIVE, lines: noted }] : [];
}

export function main(req: ClassifyRequest, io: string): void {
  let body: { schema_version: 1; files: Array<{ file: string; notes: LineNote[] }>; error: string | null };
  try {
    const wanted = req.budget?.lines ?? {};
    const files = [];
    for (const file of req.changed_files) {
      let notes: LineNote[] = [];
      const candPath = path.join(req.candidate_dir, file);
      if (existsSync(candPath)) {
        const text = readFileSync(candPath, 'utf-8');
        // A file that does not parse has no reliable structure: nothing in it is classified.
        if (parses(text, file)) notes = classify(text, file, wanted[file] ?? []);
      }
      files.push({ file, notes });
    }
    body = { schema_version: 1, files, error: null };
  } catch (e) {
    body = { schema_version: 1, files: [], error: `classify_lines crashed: ${(e as Error).message ?? String(e)}`.slice(0, 300) };
  }
  writeFileSync(`${io}/response.json`, JSON.stringify(body, null, 1));
}
