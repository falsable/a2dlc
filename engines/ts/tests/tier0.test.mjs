import { test } from 'node:test';
import assert from 'node:assert/strict';
import ts from 'typescript';
import { runTier0, policyOf, forbiddenBy, exitsLoop, parse, onlyNew, COMPILER_OPTIONS } from '../tier0.ts';
import { tree } from './helpers.mjs';

// Runs Tier 0 on a base and a candidate tree; returns the findings.
function check(base, cand, changed, budget = {}, options = {}) {
  const baseDir = tree(base);
  const candDir = tree(cand);
  return runTier0({ base_dir: baseDir, candidate_dir: candDir, changed_files: changed, budget }, options).findings;
}

const policy = (p) => ({ policy: p });
const lines = (findings, kind) => findings.filter((f) => f.kind === kind).map((f) => `${f.line}:${f.message}`);

test('typecheck: a new error is one finding and the legacy error of the base is not', () => {
  const base = { 'a.ts': 'export const legacy: number = "x";\n' };
  const cand = { 'a.ts': 'export const legacy: number = "x";\nexport const fresh: number = "y";\n' };
  const f = check(base, cand, ['a.ts']);
  assert.equal(f.length, 1, JSON.stringify(f));
  assert.equal(f[0].kind, 'typecheck');
  assert.equal(f[0].file, 'a.ts');
  assert.equal(f[0].line, 2);
  assert.match(f[0].message, /not assignable to type 'number'/);
});

test('typecheck: a bare import is any, and Node globals are not reported', () => {
  const cand = {
    'b.ts': "import { thing } from 'some-package';\nimport fs from 'node:fs';\nexport const v: number = thing.count;\nexport const t = fs.readFileSync('x');\nexport const env = process.env.X;\nexport const n = Buffer.from('a');\n",
  };
  assert.deepEqual(check({}, cand, ['b.ts']), []);
});

test('typecheck: a missing relative file is a real error and is kept', () => {
  const cand = { 'c.ts': "import { x } from './missing';\nexport const y = x;\n" };
  const f = check({}, cand, ['c.ts']);
  assert.equal(f.length, 1, JSON.stringify(f));
  assert.match(f[0].message, /missing/);
});

test('compile: a new syntax error is a compile finding, the same one in the base is not', () => {
  const broken = 'export const x = (;\n';
  const fresh = check({}, { 'd.ts': broken }, ['d.ts']);
  assert.deepEqual(fresh.map((f) => f.kind), ['compile']);
  assert.deepEqual(check({ 'd.ts': broken }, { 'd.ts': broken }, ['d.ts']), []);
});

test('policy: forbidden imports cover node: prefixes, submodules and every import form', () => {
  const cand = {
    'p.ts': [
      "import fs from 'node:fs';",
      "import p from 'fs/promises';",
      "import q from 'fsx';",
      "export { z } from 'fs';",
      "import eq = require('fs');",
      "const r = require('fs');",
      "const d = import('fs/promises');",
      'export const u = [fs, p, q, r, d, eq];',
      '',
    ].join('\n'),
  };
  const f = check({}, cand, ['p.ts'], policy({ forbidden_imports: ['fs'] }));
  const messages = lines(f, 'policy');
  assert.equal(messages.length, 6, messages.join('\n'));
  assert.ok(!messages.some((m) => m.includes('fsx')), 'fsx is a different package');
});

test('policy: forbidden import matches the module by name, not by prefix of a longer name', () => {
  assert.equal(forbiddenBy('node:fs/promises', ['fs']), 'fs');
  assert.equal(forbiddenBy('fsx', ['fs']), null);
  assert.equal(forbiddenBy('lodash/fp', ['lodash']), 'lodash');
});

test('policy: unbounded loops are flagged unless an exiting break is in the loop', () => {
  const cand = {
    'l.ts': [
      'export function a(): void { while (true) { } }',
      'export function b(x: number): void { while (true) { switch (x) { case 1: break; } } }',
      'export function c(x: number): void { while (true) { if (x) break; } }',
      'export function d(): void { for (;;) { } }',
      'export function e(): void { do { } while (1); }',
      'export function f(): void { while (true) { for (const k of [1]) { break; } } }',
      'export function g(): void { outer: while (true) { for (const k of [1]) { break outer; } } }',
      '',
    ].join('\n'),
  };
  const messages = lines(check({}, cand, ['l.ts']), 'policy');
  assert.equal(messages.length, 5, messages.join('\n'));
  assert.ok(messages.some((m) => m.startsWith('2:')), 'break inside switch does not exit');
  assert.ok(messages.some((m) => m.startsWith('6:')), 'break inside a nested loop does not exit');
  assert.ok(!messages.some((m) => m.startsWith('3:')), 'a direct break exits');
  assert.ok(!messages.some((m) => m.startsWith('7:')), 'a labeled break exits');
});

test('policy: a labeled break to a label declared inside the loop body does not leave the loop', () => {
  const cand = { 'k.ts': 'export function h(): void { while (true) { inner: { break inner; } } }\n' };
  const messages = lines(check({}, cand, ['k.ts']), 'policy');
  assert.equal(messages.length, 1, messages.join('\n'));
  assert.match(messages[0], /unbounded loop: while/);
});

test('policy: for (;true;) is the same loop as for (;;) and is flagged', () => {
  const cand = { 'f.ts': 'export function i(): void { for (;true;) { } }\nexport function j(): void { for (;1;) { } }\n' };
  const messages = lines(check({}, cand, ['f.ts']), 'policy');
  assert.equal(messages.length, 2, messages.join('\n'));
  assert.ok(messages.every((m) => /unbounded loop: for/.test(m)), messages.join('\n'));
});

for (const [label, expr] of [
  ['comma operand', '(0, eval)("1")'],
  ['array element', '[eval][0]("2")'],
  ['string index of globalThis', "globalThis['eval']('3')"],
  ['property of globalThis', 'globalThis.eval("4")'],
]) {
  test(`policy: indirect eval (${label}) is reflection, as the direct call is`, () => {
    const messages = lines(check({}, { 'e.ts': `export const v = ${expr};\n` }, ['e.ts']), 'policy');
    assert.equal(messages.length, 1, messages.join('\n'));
    assert.match(messages[0], /reflection: call to eval/);
  });
}

// A Node global that no type declares in the image is the missing package, not the Builder's code.
for (const [name, source] of [
  ['__dirname', 'export const dir = __dirname;\n'],
  ['__filename', 'export const file = __filename;\n'],
  ['exports', 'exports.x = 1;\n'],
  ['module', 'export const m = module;\n'],
  ['global', 'export const g = global;\n'],
  ['NodeJS', 'export const t: NodeJS.Timeout | undefined = undefined;\n'],
  ['setImmediate', 'export const si = setImmediate;\n'],
  ['clearImmediate', 'export const ci = clearImmediate;\n'],
]) {
  test(`typecheck: the Node global ${name} is dropped, not reported`, () => {
    assert.deepEqual(lines(check({}, { 'n.ts': source }, ['n.ts']), 'typecheck'), []);
  });
}

test('typecheck: an unknown name that is not a Node global stays a finding', () => {
  const messages = lines(check({}, { 'u.ts': 'export const u = notDeclaredAnywhere;\n' }, ['u.ts']), 'typecheck');
  assert.equal(messages.length, 1, messages.join('\n'));
  assert.match(messages[0], /Cannot find name 'notDeclaredAnywhere'/);
});

test('exitsLoop: an unlabeled break in a nested function does not count', () => {
  const sf = parse('while (true) { const f = () => { break; }; }', 'x.ts');
  const loop = sf.statements[0];
  assert.equal(exitsLoop(loop.statement), false);
});

test('policy: reflection (eval, Function, non-literal require) is flagged', () => {
  const cand = {
    'r.ts': [
      'export const a = eval("1");',
      'export const b = new Function("return 1");',
      'export const c = Function("return 2");',
      'export function d(name: string): unknown { return require(name); }',
      'export const e = require("fs");',
      '',
    ].join('\n'),
  };
  const messages = lines(check({}, cand, ['r.ts'], policy({ forbidden_imports: ['fs'] })), 'policy');
  assert.ok(messages.some((m) => m.includes('call to eval')));
  assert.ok(messages.some((m) => m.includes('call to Function')));
  assert.ok(messages.some((m) => m.includes('dynamic require with a non-literal')));
  assert.ok(messages.some((m) => m.includes('forbidden import per policy: fs')));
});

test('policy: complexity over max_complexity is a finding; the default is 15', () => {
  const branches = Array.from({ length: 15 }, (_, i) => `  if (x === ${i}) return ${i};`).join('\n');
  const cand = { 'k.ts': `export function big(x: number): number {\n${branches}\n  return -1;\n}\nexport function small(x: number): number { return x; }\n` };
  const f = lines(check({}, cand, ['k.ts']), 'policy');
  assert.deepEqual(f.map((m) => m.replace(/^\d+:/, '')), ['cyclomatic complexity 16 of big exceeds max_complexity 15']);
  assert.deepEqual(policyOf(null).max_complexity, 15);
  assert.deepEqual(policyOf({ max_complexity: null }).max_complexity, 15);
  assert.deepEqual(lines(check({}, cand, ['k.ts'], policy({ max_complexity: 20 })), 'policy'), []);
});

test('header: require_header needs the @a2dlc:slice_id line; without the flag it is not checked', () => {
  const cand = { 'h.ts': 'export const x = 1;\n', 'i.ts': '// @a2dlc:slice_id demo\nexport const y = 2;\n' };
  const f = check({}, cand, ['h.ts', 'i.ts'], policy({ require_header: true }));
  assert.deepEqual(f.map((x) => [x.kind, x.file, x.line]), [['header', 'h.ts', 1]]);
  assert.deepEqual(check({}, cand, ['h.ts', 'i.ts']), []);
});

test('policy: side-effect patterns for the four names fire on their calls only', () => {
  const cand = {
    's.ts': [
      'declare const fs: any; declare const db: any; declare const id: string;',
      'export const t1 = Date.now();',
      'export const t2 = new Date();',
      'export const t3 = new Date(0);',
      "export const w = fs.writeFile('a', 'b');",
      "export const n = fetch('https://example.invalid');",
      'export const q1 = db.query(`select ${id}`);',
      "export const q2 = db.query('select 1');",
      '',
    ].join('\n'),
  };
  const sideEffects = ['time.system_clock_direct', 'fs.write', 'network.http.outbound', 'db.raw_sql_unparameterized'];
  const f = lines(check({}, cand, ['s.ts'], policy({ forbidden_side_effects: sideEffects })), 'policy');
  const text = f.join('\n');
  assert.equal(f.length, 5, text);
  for (const name of sideEffects) assert.ok(text.includes(`${name} (`), `${name} is reported`);
  assert.ok(!text.includes('new Date(0)'), 'a Date with an argument is not the system clock');
  assert.ok(!text.includes("'select 1'"), 'a literal SQL string is not flagged');
});

test('policyOf keeps only the four side-effect names', () => {
  assert.deepEqual(policyOf({ forbidden_side_effects: ['fs.write', 'bogus'] }).forbidden_side_effects, ['fs.write']);
});

test('probes: pass, returned false, raised, raises() helper, TypeScript syntax, and the timeout', () => {
  const cand = {
    'm.ts': [
      'export const ok = (n: number): number => n + 1;',
      "export function boom(): void { throw new RangeError('x'); }",
      'export function spin(): void { while (true) { } }',
      '',
    ].join('\n'),
  };
  const probes = [
    { id: 'p1', file: 'm.ts', expr: 'ok(1) === 2' },
    { id: 'p2', file: 'm.ts', expr: 'ok(1) === 3' },
    { id: 'p3', file: 'm.ts', expr: 'boom()' },
    { id: 'p4', file: 'm.ts', expr: 'raises(RangeError, boom)' },
    { id: 'p5', file: 'm.ts', expr: '(ok as (n: number) => number)(2) === 3' },
    { id: 'p6', file: 'm.ts', expr: 'spin()' },
  ];
  const t0 = performance.now();
  const f = check({}, cand, [], { probes }, { probeTimeoutMs: 20000 });
  console.log(`probes: ${probes.length} runs in ${Math.round(performance.now() - t0)} ms`);
  const messages = f.filter((x) => x.kind === 'probe').map((x) => x.message);
  assert.deepEqual(messages.map((m) => m.split(':')[0]), ['p2', 'p3', 'p6']);
  assert.match(messages[0], /returned false$/);
  assert.match(messages[1], /raised RangeError: x$/);
  assert.match(messages[2], /exceeded 20 s$/);
});

test('tooling: a TypeScript program that does not build is a tooling finding, not silence', () => {
  const cand = { 'x.ts': 'export const x: number = "a";\n' };
  const f = check({}, cand, ['x.ts'], {}, { createProgram: () => { throw new Error('no compiler'); } });
  assert.equal(f.length, 1);
  assert.equal(f[0].kind, 'tooling');
  assert.match(f[0].message, /no compiler/);
});

test('onlyNew counts multiplicity, so a second copy of a legacy message is new', () => {
  const current = [{ message: 'a' }, { message: 'a' }, { message: 'b' }];
  assert.deepEqual(onlyNew(current, ['a']).map((x) => x.message), ['a', 'b']);
});

test('the compiler options are the strict, offline ones', () => {
  assert.equal(COMPILER_OPTIONS.strict, true);
  assert.equal(COMPILER_OPTIONS.noEmit, true);
  assert.equal(typeof ts.createProgram, 'function');
});

test('measure: Tier 0 on a six-file fixture, wall time (target 2 s)', () => {
  const files = {};
  for (let i = 0; i < 6; i++) {
    files[`m${i}.ts`] = [
      i ? `import { v${i - 1} } from './m${i - 1}';` : '',
      `export const v${i}: number = ${i};`,
      `export function f${i}(a: number, b: string): string { return b + String(a + v${i}); }`,
      `export class C${i} { m(x: number): number { return x * ${i}; } }`,
      '',
    ].join('\n');
  }
  // The base has the same files: both trees are type-checked in one program, as in a real round.
  const base = tree(files);
  const cand = tree(files);
  const changed = Object.keys(files);
  const t0 = performance.now();
  const r = runTier0({ base_dir: base, candidate_dir: cand, changed_files: changed, budget: {} });
  const ms = Math.round(performance.now() - t0);
  console.log(`tier0 fixture: ${changed.length} files in base and candidate, ${ms} ms, findings=${r.findings.length}`);
  assert.deepEqual(r.findings, []);
  assert.ok(ms < 2000, `tier0 took ${ms} ms, target 2000 ms`);
});
