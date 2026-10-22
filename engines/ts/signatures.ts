// operation: signatures of the TypeScript engine (Saboteur, protocol §3): the candidate's public signatures, without
// bodies. input.mjs dispatches it; engine.ts is not touched because its sha256 (engine-v2.sha256) pins the v2
// generators measured. Output contract: contracts/engine-signatures-response.schema.json.
//
// Symbols: exported functions and function constants, exported classes and their public methods (Class.method, and
// Class.constructor). The signature comes from the TypeScript checker: what the code declares is copied as is, and
// what it does not (typeText of returnType or of an unannotated parameter) is inferred by the checker. Default values
// and bodies are left out: they would be Builder code.
import ts from 'typescript';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';

const IO = process.env.A2DLC_IO ?? '/io';
const req = JSON.parse(readFileSync(`${IO}/request.json`, 'utf-8'));

interface SymbolInfo { name: string; signature: string; doc: string | null; lines: [number, number] }

const EXT = /\.(ts|tsx|mts|cts)$/;
const files: string[] = (req.changed_files as string[]).filter(
  (f) => EXT.test(f) && !f.endsWith('.d.ts') && existsSync(`${req.candidate_dir}/${f}`));

const program = ts.createProgram(files.map((f) => `${req.candidate_dir}/${f}`), {
  noResolve: true, skipLibCheck: true, target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext,
  jsx: ts.JsxEmit.Preserve, allowJs: false, noEmit: true, types: [],
});
const checker = program.getTypeChecker();
const FLAGS = ts.TypeFormatFlags.NoTruncation | ts.TypeFormatFlags.UseAliasDefinedOutsideCurrentScope;

const typeText = (t: ts.Type, at: ts.Node) => checker.typeToString(t, at, FLAGS);

function doc(n: ts.Node): string | null {
  const docs = (n as unknown as { jsDoc?: ts.JSDoc[] }).jsDoc;
  const d = docs?.[docs.length - 1]?.comment;
  const text = typeof d === 'string' ? d : (d ?? []).map((p) => p.text).join('');
  const first = text.trim().split(/\n\s*\n/)[0]?.trim().replace(/\s*\n\s*/g, ' ').slice(0, 300);
  return first ? first : null;
}

function typeParameters(tps: ts.NodeArray<ts.TypeParameterDeclaration> | undefined, sf: ts.SourceFile): string {
  return tps && tps.length ? `<${tps.map((t) => t.getText(sf)).join(', ')}>` : '';
}

// Parameters without default values: `x = 5` becomes `x?: number` (the default value is code).
function parameters(fn: ts.SignatureDeclaration, sf: ts.SourceFile): string {
  return fn.parameters.map((p) => {
    const name = p.name.getText(sf);
    const optional = !!p.questionToken || !!p.initializer;
    const annotated = p.type ? p.type.getText(sf) : typeText(checker.getTypeAtLocation(p), p);
    return `${p.dotDotDotToken ? '...' : ''}${name}${optional && !p.dotDotDotToken ? '?' : ''}: ${annotated}`;
  }).join(', ');
}

function returnType(fn: ts.SignatureDeclaration, sf: ts.SourceFile): string {
  if (fn.type) return fn.type.getText(sf);
  const sig = checker.getSignatureFromDeclaration(fn);
  return sig ? typeText(checker.getReturnTypeOfSignature(sig), fn) : 'unknown';
}

const isPublic = (m: ts.ClassElement): boolean => {
  const modifiers = ts.getCombinedModifierFlags(m as ts.Declaration);
  if (modifiers & (ts.ModifierFlags.Private | ts.ModifierFlags.Protected)) return false;
  return !(m.name && ts.isPrivateIdentifier(m.name));
};

const lines = (n: ts.Node, sf: ts.SourceFile): [number, number] => [
  sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, sf.getLineAndCharacterOfPosition(n.getEnd()).line + 1];

function symbols(sf: ts.SourceFile): SymbolInfo[] {
  const out: SymbolInfo[] = [];
  const exportedByName = new Set<string>();
  for (const st of sf.statements) {
    if (ts.isExportDeclaration(st) && !st.moduleSpecifier && st.exportClause && ts.isNamedExports(st.exportClause)) {
      for (const e of st.exportClause.elements) exportedByName.add((e.propertyName ?? e.name).text);
    }
  }
  const isExported = (n: ts.Node, name: string) =>
    !!(ts.getCombinedModifierFlags(n as ts.Declaration) & ts.ModifierFlags.Export) || exportedByName.has(name);
  const prefix = (n: ts.Node) => (ts.getCombinedModifierFlags(n as ts.Declaration) & ts.ModifierFlags.Default ? 'export default ' : 'export ');
  const isAsync = (n: ts.Node) => (ts.getCombinedModifierFlags(n as ts.Declaration) & ts.ModifierFlags.Async ? 'async ' : '');

  for (const st of sf.statements) {
    if (ts.isFunctionDeclaration(st) && st.name && isExported(st, st.name.text)) {
      const f = `${prefix(st)}${isAsync(st)}function${st.asteriskToken ? '*' : ''} ${st.name.text}`;
      out.push({ name: st.name.text, signature: `${f}${typeParameters(st.typeParameters, sf)}(${parameters(st, sf)}): ${returnType(st, sf)}`,
        doc: doc(st), lines: lines(st, sf) });
    } else if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) {
        const init = d.initializer;
        if (!ts.isIdentifier(d.name) || !init || !(ts.isArrowFunction(init) || ts.isFunctionExpression(init)) || !isExported(st, d.name.text)) continue;
        const mark = st.declarationList.flags & ts.NodeFlags.Const ? 'const' : (st.declarationList.flags & ts.NodeFlags.Let ? 'let' : 'var');
        out.push({ name: d.name.text,
          signature: `export ${mark} ${d.name.text} = ${isAsync(init)}${typeParameters(init.typeParameters, sf)}(${parameters(init, sf)}) => ${returnType(init, sf)}`,
          doc: doc(st), lines: lines(st, sf) });
      }
    } else if (ts.isClassDeclaration(st) && st.name && isExported(st, st.name.text)) {
      const name = st.name.text;
      const inheritance = (st.heritageClauses ?? []).map((h) => ` ${h.token === ts.SyntaxKind.ExtendsKeyword ? 'extends' : 'implements'} ${h.types.map((t) => t.getText(sf)).join(', ')}`).join('');
      out.push({ name: name, signature: `${prefix(st)}${ts.getCombinedModifierFlags(st) & ts.ModifierFlags.Abstract ? 'abstract ' : ''}class ${name}${typeParameters(st.typeParameters, sf)}${inheritance}`,
        doc: doc(st), lines: lines(st, sf) });
      for (const m of st.members) {
        if (!isPublic(m)) continue;
        const staticPrefix = ts.getCombinedModifierFlags(m as ts.Declaration) & ts.ModifierFlags.Static ? 'static ' : '';
        if (ts.isConstructorDeclaration(m)) {
          out.push({ name: `${name}.constructor`, signature: `constructor(${parameters(m, sf)})`, doc: doc(m), lines: lines(m, sf) });
        } else if (ts.isMethodDeclaration(m) && (ts.isIdentifier(m.name) || ts.isStringLiteral(m.name))) {
          out.push({ name: `${name}.${m.name.text}`,
            signature: `${staticPrefix}${isAsync(m)}${m.name.text}${m.questionToken ? '?' : ''}${typeParameters(m.typeParameters, sf)}(${parameters(m, sf)}): ${returnType(m, sf)}`,
            doc: doc(m), lines: lines(m, sf) });
        }
      }
    }
  }
  return out;
}

const files = [];
for (const f of files) {
  const sf = program.getSourceFile(`${req.candidate_dir}/${f}`);
  if (sf) files.push({ file: f, symbols: symbols(sf) });
}
writeFileSync(`${IO}/response.json`, JSON.stringify({ schema_version: 1, files, error: null }, null, 1));
