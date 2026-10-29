// Operation classify_lines (docs/COVERAGE_NOTES.md): the changed lines that V8 plus source maps report with no hits only
// because the TypeScript compiler erased them (type aliases, interfaces, type-only imports and exports, ambient
// declarations) are noted as declaration-only. Every other line stays out of the answer, so the core keeps it blocking.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { classify, main } from '../classify.ts';
import { tree } from './helpers.mjs';

const DECLARATIVE = 'declarative_only';

// Every line of the source, 1-based: the worst case, where V8 reported the whole file at zero hits.
const all = (source) => Array.from({ length: source.split('\n').length }, (_, i) => i + 1);
const noted = (source, lines = all(source), file = 'm.ts') =>
  Object.fromEntries(classify(source, file, lines).map((n) => [n.category, n.lines]));

test('the Entropos case: a changed type alias with no hits is a declaration-only note', () => {
  const source = [
    "import { guard } from './guard';",
    '',
    "type Protocol = 'openai' | 'anthropic' | 'mcp';",
    '',
    'export const run = (p: Protocol) => guard(p);',
  ].join('\n');
  assert.deepEqual(noted(source, [3]), { [DECLARATIVE]: [3] });
});

test('type alias, interface and type-only import and export: every line of each is declaration-only', () => {
  const source = [
    "import type { A } from './a';", //                   1
    "import { type B, type C } from './b';", //           2
    "import type D from './d';", //                       3
    "export type { A, B };", //                           4
    "export type * from './e';", //                       5
    "export { type C } from './b';", //                   6
    'type Pair<T> = {', //                                7
    '  left: T;', //                                      8
    '  right: T;', //                                     9
    '};', //                                              10
    'export interface Options extends Base {', //         11
    '  name: string;', //                                 12
    '  run(input: Pair<number>): void;', //               13
    '}', //                                               14
    'export default interface Shape { sides: number }', // 15
  ].join('\n');
  assert.deepEqual(noted(source), { [DECLARATIVE]: all(source) });
});

test('ambient declarations are erased too', () => {
  const source = [
    'declare const VERSION: string;', //              1
    'declare function load(path: string): Buffer;', // 2
    "declare module 'x' {", //                          3
    '  export const y: number;', //                     4
    '}', //                                             5
    'export declare class Foo { bar(): void }', //      6
    'declare global {', //                              7
    '  interface Window { a2dlc: boolean }', //         8
    '}', //                                             9
  ].join('\n');
  assert.deepEqual(noted(source), { [DECLARATIVE]: all(source) });
});

test('a changed executable line with no hits stays unclassified', () => {
  const source = [
    'export const LIMIT = 10;', //                         1
    'export function add(a: number, b: number): number {', // 2
    '  return a + b;', //                                  3
    '}', //                                                4
    "import { guard } from './guard';", //                  5 a value import runs at load time: left to the tests
    "import './polyfill';", //                              6 a side-effect import
    "import { type A, b } from './ab';", //                 7 b is a value
    'export enum Mode { On, Off }', //                      8 an enum is emitted code
    'namespace N { export const x = 1; }', //               9
    'abstract class K { abstract f(): void }', //           10
  ].join('\n');
  assert.deepEqual(noted(source), {});
});

test('a line shared with executable code is not classified, whichever side comes first', () => {
  const source = [
    'type A = number; const a: A = 1;', //     1
    'const b = 2; type B = string;', //         2
    'type C = number; type D = string;', //     3 only types share this one
    'const t = `x`; interface E { x: 1 }', //   4
  ].join('\n');
  assert.deepEqual(noted(source), { [DECLARATIVE]: [3] });
});

test('a type declared inside a function body is erased as well, and the code around it is not', () => {
  const source = [
    'export function f(x: number) {', //  1
    '  type Local = { v: number };', //   2
    '  const y: Local = { v: x };', //    3
    '  return y.v;', //                   4
    '}', //                               5
  ].join('\n');
  assert.deepEqual(noted(source), { [DECLARATIVE]: [2] });
});

test('only the lines the core asked about come back, and a line in a multi-line type is asked line by line', () => {
  const source = ['type Pair<T> = {', '  left: T;', '  right: T;', '};', 'export const a = 1;'].join('\n');
  assert.deepEqual(noted(source, [2, 3]), { [DECLARATIVE]: [2, 3] });
  assert.deepEqual(noted(source, [5]), {});
  assert.deepEqual(noted(source, []), {});
});

test('a comment above or beside a type does not turn its line into code', () => {
  const source = ['/** The protocols. */', "type P = 'a' | 'b'; // keep in sync", '// done'].join('\n');
  assert.deepEqual(noted(source, [2]), { [DECLARATIVE]: [2] });
});

test('the Entropos layout: V8 reports zero hits on the blank line after the erased alias, and that line is noted', () => {
  // Measured with the real candidate (budgetGuard.ts): DA:76 had 16 hits (the alias), DA:77 had 0 (the blank line after it).
  const source = [
    'export function evaluate() {', //                1
    '  return 1;', //                                 2
    '}', //                                           3
    '', //                                            4
    "type Protocol = 'openai' | 'anthropic' | 'mcp';", // 5
    '', //                                            6
    'function reject(p: Protocol) {', //              7
    '  return p;', //                                 8
    '}', //                                           9
  ].join('\n');
  assert.deepEqual(noted(source, [4, 6]), { [DECLARATIVE]: [4, 6] });
  assert.deepEqual(noted(source, [5, 7, 8]), { [DECLARATIVE]: [5] });
});

test('a line with no token on it computes nothing: blank and comment-only lines are noted, code and template text are not', () => {
  const source = [
    '// header', //                1 comment
    'const a = 1;', //             2 code
    '', //                         3 blank
    '/* block', //                 4 comment
    '   continues */', //          5 comment
    'const t = `x', //             6 a template literal starts: code
    '', //                         7 blank inside the template: its text, not a blank line
    'y`;', //                      8 code
    'const b = 2; // tail', //     9 code with a trailing comment
  ].join('\n');
  assert.deepEqual(noted(source), { [DECLARATIVE]: [1, 3, 4, 5] });
  assert.deepEqual(noted(source, [0, 10, 99]), {});
});

test('only TypeScript sources are classified: the same text in a .js file is left alone', () => {
  assert.deepEqual(noted("type A = 'a';", [1], 'm.js'), {});
  assert.deepEqual(noted("type A = 'a';", [1], 'm.mjs'), {});
  assert.deepEqual(noted("type A = 'a';", [1], 'view.tsx'), { [DECLARATIVE]: [1] });
  assert.deepEqual(noted("type A = 'a';", [1], 'm.mts'), { [DECLARATIVE]: [1] });
});

test('main answers every changed file, with no notes for a file it does not classify or cannot read', () => {
  const cand = tree({ 'src/types.ts': "export type P = 'a';\nexport const a = 1;\n", 'src/legacy.js': 'var a = 1;\n' });
  const io = tree({});
  main({ changed_files: ['src/types.ts', 'src/legacy.js', 'src/gone.ts'], candidate_dir: cand, budget: { lines: {
    'src/types.ts': [1, 2], 'src/legacy.js': [1], 'src/gone.ts': [1],
  } } }, io);
  const response = JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8'));
  assert.deepEqual(response, {
    schema_version: 1,
    files: [
      { file: 'src/types.ts', notes: [{ category: DECLARATIVE, lines: [1] }] },
      { file: 'src/legacy.js', notes: [] },
      { file: 'src/gone.ts', notes: [] },
    ],
    error: null,
  });
});

test('a file that does not parse is left unclassified: the core keeps it blocking', () => {
  const cand = tree({ 'bad.ts': 'type A = ;\nexport const = ;\n' });
  const io = tree({});
  main({ changed_files: ['bad.ts'], candidate_dir: cand, budget: { lines: { 'bad.ts': [1, 2] } } }, io);
  assert.ok(existsSync(path.join(io, 'response.json')));
  assert.deepEqual(JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8')).files, [{ file: 'bad.ts', notes: [] }]);
});
