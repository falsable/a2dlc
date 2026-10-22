// Tests of engines/ts/mutate.ts. Run with the TypeScript engine image (node 22, tsx and typescript installed):
//   node --import tsx --test tests/mutate.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  mutantsOf, parseErrors, runMutate,
  REASON_ASSERT, REASON_BRANCH, REASON_BRANCH_TYPES, REASON_LOG,
} from '../mutate.ts';

const OPERATORS = new Set(['arithmetic', 'comparison', 'negate_condition', 'constant', 'return_none', 'remove_call']);

// Runs the operation on one file. `files` maps each candidate path to its text; `requested` maps path to changed lines.
function run(files, requested, maxMutants) {
  const dir = mkdtempSync(join(tmpdir(), 'mutate-cand-'));
  const io = mkdtempSync(join(tmpdir(), 'mutate-io-'));
  for (const [path, text] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, path)), { recursive: true });
    writeFileSync(join(dir, path), text, 'utf-8');
  }
  runMutate({
    candidate_dir: dir,
    changed_files: Object.keys(files),
    budget: { changed_lines: requested, max_mutants: maxMutants },
  }, io);
  const response = JSON.parse(readFileSync(join(io, 'response.json'), 'utf-8'));
  const read = (m) => readFileSync(join(io, 'mutants', m.id, m.file), 'utf-8');
  return { response, io, read };
}

// The mutants of a single file, with their mutated text.
function mutantsFor(text, lines, maxMutants = 30) {
  const { response, read } = run({ 'src.ts': text }, { 'src.ts': lines }, maxMutants);
  return { response, mutated: response.mutants.map((m) => ({ ...m, text: read(m) })) };
}

const texts = (r) => r.mutated.map((m) => m.text);

test('response has exactly the schema fields and valid operators', () => {
  const { response } = mutantsFor('export const a = 1 + 2;\n', [1]);
  assert.deepEqual(Object.keys(response).sort(), ['error', 'mutants', 'not_generated', 'pruned', 'schema_version']);
  assert.equal(response.error, null);
  assert.equal(response.schema_version, 1);
  for (const m of response.mutants) {
    assert.ok(OPERATORS.has(m.operator), m.operator);
    assert.match(m.id, /^m\d{3}$/);
  }
});

test('arithmetic: + becomes -, the mutant is the whole file', () => {
  const { response, mutated } = mutantsFor('export const a = 1 + 2;\n', [1]);
  const arith = response.mutants.filter((m) => m.operator === 'arithmetic');
  assert.equal(arith.length, 1);
  assert.equal(arith[0].description, 'arithmetic operator + changed to -');
  assert.ok(texts({ mutated }).includes('export const a = 1 - 2;\n'));
});

test('arithmetic: ** becomes * inside parentheses so the precedence holds', () => {
  const { mutated } = mutantsFor('export const a = 2 ** 3 + 1;\n', [1]);
  const pow = mutated.find((m) => m.description === 'arithmetic operator ** changed to *');
  assert.equal(pow.text, 'export const a = (2 * 3) + 1;\n');
});

test('arithmetic: compound assignment += becomes -=', () => {
  const { mutated } = mutantsFor('let x = 0;\nx += 2;\n', [2]);
  assert.ok(texts({ mutated }).includes('let x = 0;\nx -= 2;\n'));
});

test('comparison: ===, !==, == and < change to their pair', () => {
  const { mutated } = mutantsFor('export const f = (a: number, b: number) => a === b && a < b;\n', [1]);
  const want = ['comparison === changed to !==', 'comparison < changed to <='];
  for (const d of want) assert.ok(mutated.some((m) => m.description === d), d);
  const eq = mutantsFor('export const f = (a: number) => a !== 1;\n', [1]);
  assert.equal(eq.mutated[0].text, 'export const f = (a: number) => a === 1;\n');
  const loose = mutantsFor('export const f = (a: number) => a == 1;\n', [1]);
  assert.equal(loose.mutated[0].text, 'export const f = (a: number) => a != 1;\n');
});

test('negate_condition: if, while, do-while and the conditional expression', () => {
  const src = 'export function f(x: boolean, y: boolean): number {\n  if (x) { return 1; }\n  while (y) { y = false; }\n'
    + '  do { y = true; } while (y);\n  return x ? 2 : 3;\n}\n';
  const { mutated } = mutantsFor(src, [2, 3, 4, 5]);
  const negated = mutated.filter((m) => m.operator === 'negate_condition').map((m) => m.description);
  assert.deepEqual(negated.sort(), [
    'condition of a conditional expression negated', 'condition of a do-while negated',
    'condition of a while negated', 'condition of an if negated',
  ]);
  assert.ok(texts({ mutated }).some((t) => t.includes('if ((!(x)))')));
});

test('constant: numeric +1 and -1, inverted boolean, string changes', () => {
  const { mutated } = mutantsFor("export const n = 5;\nexport const s = 'abc';\nexport const b = true;\nexport const e = '';\n",
    [1, 2, 3, 4]);
  const d = mutated.map((m) => m.description);
  assert.ok(d.includes('numeric constant changed: +1'));
  assert.ok(d.includes('numeric constant changed: -1'));
  assert.ok(d.includes('boolean inverted'));
  assert.ok(d.includes('non-empty string changed to empty'));
  assert.ok(d.includes('empty string changed to non-empty'));
  const minus = mutated.find((m) => m.description === 'numeric constant changed: -1');
  assert.equal(minus.text.split('\n')[0], 'export const n = 4;');
  const neg = mutantsFor('export const n = 0;\n', [1]);
  assert.ok(texts(neg).includes('export const n = (-1);\n'));
});

test('return_none: a returned value becomes return undefined; return undefined is not mutated', () => {
  const src = 'export function f(x: number): number {\n  return x;\n}\nexport function g(): void {\n  return undefined;\n}\n';
  const { mutated } = mutantsFor(src, [2, 5]);
  const ret = mutated.filter((m) => m.operator === 'return_none');
  assert.equal(ret.length, 1);
  assert.ok(ret[0].text.includes('  return undefined;\n}\nexport function g'));
});

test('remove_call: a call statement is removed, an awaited call too', () => {
  const src = 'export async function f(): Promise<void> {\n  helper();\n  await helper();\n}\nfunction helper() {}\n';
  const { mutated } = mutantsFor(src, [2, 3]);
  const removed = mutated.filter((m) => m.operator === 'remove_call');
  assert.equal(removed.length, 2);
  assert.ok(removed.some((m) => m.text.includes('\n  ;\n  await helper();')));
});

test('only the requested lines are mutated', () => {
  const { response } = mutantsFor('export const a = 1 + 2;\nexport const b = 3 + 4;\n', [2]);
  assert.ok(response.mutants.length > 0);
  assert.ok(response.mutants.every((m) => m.line === 2));
});

test('no requested lines: nothing is mutated and nothing is reported', () => {
  const { response } = run({ 'src.ts': 'export const a = 1 + 2;\n' }, {}, 30);
  assert.deepEqual([response.mutants, response.not_generated, response.pruned], [[], [], []]);
});

test('types, interfaces and type aliases are not mutated', () => {
  const src = 'interface I { a: 3 }\ntype T = 4;\nlet v: 5 = 5;\nexport const w = (1 + 2) as number;\n';
  const { response } = mutantsFor(src, [1, 2, 3, 4]);
  const onLine3 = response.mutants.filter((m) => m.line === 3);
  assert.ok(onLine3.every((m) => m.operator === 'constant'));
  assert.equal(onLine3.length, 2, 'only the initializer 5, not the type 5');
  assert.equal(response.mutants.filter((m) => m.line <= 2).length, 0);
  assert.ok(response.mutants.some((m) => m.line === 4 && m.operator === 'arithmetic'));
});

test('strings of import specifiers and directives are not mutated', () => {
  const src = "import { x } from './a';\n'use strict';\nexport const y = x;\n";
  const { response } = mutantsFor(src, [1, 2]);
  assert.equal(response.mutants.length, 0);
});

test('splices by UTF-8 bytes: multibyte text before the change keeps its bytes', () => {
  const src = "export const s = 'ñaña€';\nexport const y = 1 + 2;\n";
  const { mutated } = mutantsFor(src, [2]);
  const arith = mutated.find((m) => m.operator === 'arithmetic');
  assert.equal(arith.text, "export const s = 'ñaña€';\nexport const y = 1 - 2;\n");
});

test('a BOM is kept in the mutant bytes', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mutate-bom-'));
  const io = mkdtempSync(join(tmpdir(), 'mutate-bom-io-'));
  writeFileSync(join(dir, 'src.ts'), Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from('export const a = 1 + 2;\n')]));
  runMutate({ candidate_dir: dir, changed_files: ['src.ts'], budget: { changed_lines: { 'src.ts': [1] } } }, io);
  const response = JSON.parse(readFileSync(join(io, 'response.json'), 'utf-8'));
  const bytes = readFileSync(join(io, 'mutants', response.mutants[0].id, 'src.ts'));
  assert.deepEqual([...bytes.subarray(0, 3)], [0xef, 0xbb, 0xbf]);
});

test('pruning: console.* statements as logging, console.assert as assert', () => {
  const src = 'export function f(a: number, b: number): void {\n  console.log(a + b);\n  console.assert(a - b > 0, "x");\n}\n';
  const { response } = mutantsFor(src, [2, 3]);
  assert.equal(response.mutants.length, 0);
  const reasons = response.pruned.map((p) => `${p.line}:${p.reason}`).sort();
  assert.deepEqual(reasons, [`2:${REASON_LOG}`, `3:${REASON_ASSERT}`]);
  assert.ok(response.pruned.every((p) => p.file === 'src.ts'));
});

test('pruning: the dead branch of a literal condition', () => {
  const src = 'export function f(): number {\n  let y = 0;\n  if (false) { y = 1 + 2; }\n  if (0) { y = 3 + 4; }\n'
    + '  while (false) { y = 5 + 6; }\n  return y;\n}\n';
  const { response } = mutantsFor(src, [3, 4, 5]);
  // The conditions themselves stay mutable (negating `false` is a real change); the bodies are pruned.
  assert.equal(response.mutants.filter((m) => m.operator === 'arithmetic').length, 0);
  assert.deepEqual(response.pruned.map((p) => [p.line, p.reason]), [[3, REASON_BRANCH], [4, REASON_BRANCH], [5, REASON_BRANCH]]);
});

test('pruning: the else of if(true) is dead, the then branch is not', () => {
  const src = 'export function f(): number {\n  let y = 0;\n  if (true) { y = 1 + 2; } else { y = 3 + 4; }\n  return y;\n}\n';
  const { response } = mutantsFor(src, [3]);
  assert.deepEqual(response.pruned, [{ file: 'src.ts', line: 3, reason: REASON_BRANCH }]);
  assert.ok(response.mutants.some((m) => m.description === 'arithmetic operator + changed to -'));
});

test('pruning: a literal true condition without else prunes nothing', () => {
  const src = 'export function f(): number {\n  let y = 0;\n  if (true) { y = 1 + 2; }\n  return y;\n}\n';
  const { response } = mutantsFor(src, [3]);
  assert.equal(response.pruned.length, 0);
  assert.ok(response.mutants.length > 0);
});

test('pruning: a const typed as literal false prunes its branch with the type reason', () => {
  const src = 'export function f(): number {\n  const DEBUG = false;\n  let y = 0;\n  if (DEBUG) { y = 1 + 2; }\n  return y;\n}\n';
  const { response } = mutantsFor(src, [4]);
  assert.equal(response.mutants.filter((m) => m.operator === 'arithmetic').length, 0);
  assert.deepEqual(response.pruned, [{ file: 'src.ts', line: 4, reason: REASON_BRANCH_TYPES }]);
});

test('pruning: a let narrowed to literal false is NOT pruned (closures can change it)', () => {
  const src = 'export function f(): number {\n  let flag = false;\n  let y = 0;\n  if (flag) { y = 1 + 2; }\n  return y;\n}\n';
  const { response } = mutantsFor(src, [4]);
  assert.equal(response.pruned.length, 0);
  assert.ok(response.mutants.some((m) => m.operator === 'arithmetic'));
});

test('parse failures of the original file go to not_generated, with no mutants', () => {
  const { response } = run({ 'src.ts': 'export const a = (1 + ;\n' }, { 'src.ts': [1] }, 30);
  assert.equal(response.mutants.length, 0);
  assert.equal(response.not_generated.length, 1);
  assert.equal(response.not_generated[0].file, 'src.ts');
  assert.match(response.not_generated[0].reason, /^does not parse: /);
});

test('mutants that do not compile go to not_generated (fake parse check)', () => {
  const text = 'export const a = 1 + 2;\nexport const b = 3 - 4;\n';
  const seen = [];
  const analysis = mutantsOf(text, 'src.ts', new Set([1, 2]), {
    parseErrorsOf: (t) => {
      seen.push(t);
      return t.includes('1 - 2') ? ['fake syntax error'] : [];
    },
  });
  assert.ok(seen.length > 0, 'every mutant is parse-checked');
  assert.ok(analysis.uncompilable.some((u) => u.line === 1 && u.description === 'arithmetic operator + changed to -'));
  assert.ok(!analysis.mutants.some((m) => m.description === 'arithmetic operator + changed to -'));
  assert.ok(analysis.mutants.some((m) => m.line === 2));
});

test('max_mutants caps the mutants and says what did not fit', () => {
  const src = 'export const a = 1 + 2 + 3;\n';
  const { response } = mutantsFor(src, [1], 2);
  assert.equal(response.mutants.length, 2);
  const cap = response.not_generated.find((n) => n.reason.startsWith('max_mutants=2:'));
  assert.ok(cap, 'a not_generated entry names the cap');
  assert.match(cap.reason, /mutants not generated \(lines 1\)$/);
});

test('a file that is not TypeScript is not generated, and the mutants are ids in order across files', () => {
  const { response } = run({
    'a.ts': 'export const a = 1 + 2;\n',
    'b.ts': 'export const b = 3 * 4;\n',
    'c.py': 'x = 1 + 2\n',
  }, { 'a.ts': [1], 'b.ts': [1], 'c.py': [1] }, 30);
  assert.deepEqual(response.not_generated, [{ file: 'c.py', reason: 'not TypeScript: this engine does not mutate it' }]);
  assert.deepEqual(response.mutants.map((m) => m.id), response.mutants.map((_, i) => `m${String(i + 1).padStart(3, '0')}`));
  assert.ok(response.mutants.some((m) => m.file === 'b.ts'));
});

test('a file that has no requested lines is skipped silently', () => {
  const { response } = run({ 'a.ts': 'export const a = 1 + 2;\n', 'b.ts': 'export const b = 1;\n' },
    { 'a.ts': [1] }, 30);
  assert.equal(response.not_generated.length, 0);
  assert.ok(response.mutants.every((m) => m.file === 'a.ts'));
});

test('a .d.ts file yields no mutants (no code)', () => {
  const { response } = run({ 'types.d.ts': 'export declare const a: 1;\n' }, { 'types.d.ts': [1] }, 30);
  assert.equal(response.mutants.length, 0);
  assert.equal(response.error, null);
});

test('a numeric literal that overflows to Infinity is not mutated', () => {
  const { response } = mutantsFor('export const big = 1e400;\n', [1]);
  assert.equal(response.mutants.filter((m) => m.operator === 'constant').length, 0);
});

test('parseErrors reports syntax errors and nothing for valid code', () => {
  assert.deepEqual(parseErrors('x.ts', 'export const a = 1;\n'), []);
  assert.ok(parseErrors('x.ts', 'export const a = (1 + ;\n').length > 0);
});

test('a tsx file is parsed as tsx', () => {
  const { response, read } = run({ 'view.tsx': 'export const C = () => <div>{1 + 2}</div>;\n' }, { 'view.tsx': [1] }, 30);
  assert.equal(response.not_generated.length, 0);
  const arith = response.mutants.find((m) => m.operator === 'arithmetic');
  assert.ok(arith, 'the + inside the JSX expression is mutated');
  assert.equal(read(arith), 'export const C = () => <div>{1 - 2}</div>;\n');
});

test('await of a non-call (await p;) is not removed; an awaited call is', () => {
  const src = 'export async function f(p: Promise<void>): Promise<void> {\n  await p;\n  await g();\n}\nasync function g() {}\n';
  const { response } = mutantsFor(src, [2, 3]);
  const removed = response.mutants.filter((m) => m.operator === 'remove_call');
  assert.deepEqual(removed.map((m) => m.line), [3]);
});

test('require() and import() specifiers are not mutated, a plain string argument still is', () => {
  const src = "export async function f(): Promise<unknown[]> {\n  const m = require('./a');\n"
    + "  const n = await import('./b');\n  return [m, n];\n}\nexport const s = f('abc');\n";
  const { response } = mutantsFor(src, [2, 3, 6]);
  assert.equal(response.mutants.filter((m) => m.line === 2 || m.line === 3).length, 0);
  assert.ok(response.mutants.some((m) => m.line === 6 && m.description === 'non-empty string changed to empty'));
});

test('a directory named as the changed file sets error and leaves the lists empty', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mutate-dir-'));
  const io = mkdtempSync(join(tmpdir(), 'mutate-dir-io-'));
  mkdirSync(join(dir, 'src.ts'));
  runMutate({ candidate_dir: dir, changed_files: ['src.ts'], budget: { changed_lines: { 'src.ts': [1] } } }, io);
  const response = JSON.parse(readFileSync(join(io, 'response.json'), 'utf-8'));
  assert.equal(typeof response.error, 'string');
  assert.deepEqual([response.mutants, response.not_generated, response.pruned], [[], [], []]);
});

test('an error on the second file leaves no mutant file under io/mutants', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mutate-second-'));
  const io = mkdtempSync(join(tmpdir(), 'mutate-second-io-'));
  writeFileSync(join(dir, 'a.ts'), 'export const a = 1 + 2;\n', 'utf-8');
  mkdirSync(join(dir, 'b.ts'));
  runMutate({
    candidate_dir: dir, changed_files: ['a.ts', 'b.ts'],
    budget: { changed_lines: { 'a.ts': [1], 'b.ts': [1] } },
  }, io);
  const response = JSON.parse(readFileSync(join(io, 'response.json'), 'utf-8'));
  assert.equal(typeof response.error, 'string');
  assert.equal(response.mutants.length, 0);
  assert.equal(existsSync(join(io, 'mutants')), false, 'no mutant folder is left behind');
});

test('invalid UTF-8 goes to not_generated as unsupported encoding', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mutate-enc-'));
  const io = mkdtempSync(join(tmpdir(), 'mutate-enc-io-'));
  writeFileSync(join(dir, 'src.ts'), Buffer.from([0x65, 0x78, 0x70, 0xff, 0xfe, 0x0a]));
  runMutate({ candidate_dir: dir, changed_files: ['src.ts'], budget: { changed_lines: { 'src.ts': [1] } } }, io);
  const response = JSON.parse(readFileSync(join(io, 'response.json'), 'utf-8'));
  assert.equal(response.error, null);
  assert.equal(response.mutants.length, 0);
  assert.equal(response.not_generated.length, 1);
  assert.ok(response.not_generated[0].reason.startsWith('unsupported encoding'), response.not_generated[0].reason);
});
