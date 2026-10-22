import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { stubOf, main } from '../stub.ts';
import { tree } from './helpers.mjs';

// Writes a stub text as a module of its own and imports it (tsx loads it; the stub text is plain JavaScript).
async function load(text, name = 'stubbed.mjs') {
  const dir = mkdtempSync(path.join(tmpdir(), 'a2dlc-stub-'));
  const file = path.join(dir, name);
  writeFileSync(file, text);
  return import(pathToFileURL(file).href);
}

test('a new module: every exported function, class and value is stubbed; calls throw "a2dlc stub"', async () => {
  const cand = [
    'export function hello(n: number): string { return "hi" + n; }',
    'export class Box { constructor(public v: number) {} size(): number { return this.v; } }',
    'export const LIMIT = 10;',
    'const hidden = 1;',
    '',
  ].join('\n');
  const r = stubOf(null, cand, 'new.ts');
  assert.deepEqual(r.symbols, ['hello', 'Box', 'LIMIT']);
  assert.deepEqual(r.unstubbed, []);
  const mod = await load(r.text);
  assert.throws(() => mod.hello(1), /a2dlc stub/);
  assert.throws(() => new mod.Box(1), /a2dlc stub/);
  assert.equal(mod.LIMIT, undefined);
});

test('an existing module keeps the base bodies and gets new exports only', async () => {
  const base = 'export function f(n: number) { return n; }\nconst hidden = 1;\n';
  const cand = 'export function f(n: number) { return n * 2; }\nexport function g() { return 1; }\nconst hidden = 2;\n';
  const r = stubOf(base, cand, 'm.ts');
  assert.deepEqual(r.symbols, ['g']);
  assert.ok(r.text.startsWith(base.trimEnd()), 'the base text is kept as it is');
  assert.ok(!r.text.includes('n * 2'), 'the candidate body of f is not copied');
  const mod = await load(r.text, 'm.ts');
  assert.equal(mod.f(3), 3, 'the base body of f runs');
  assert.throws(() => mod.g(), /a2dlc stub/);
});

test('a new method of an existing class is patched on, and the base method still runs', async () => {
  const base = 'export class K { a() { return 1; } }\n';
  const cand = 'export class K { a() { return 1; } b() { return 2; } }\n';
  const r = stubOf(base, cand, 'k.ts');
  assert.deepEqual(r.symbols, ['K.b']);
  const mod = await load(r.text, 'k.mjs');
  assert.equal(new mod.K().a(), 1);
  assert.throws(() => new mod.K().b(), /a2dlc stub/);
});

test('a new accessor on an existing class is unstubbed (a patch cannot stand for it)', () => {
  const base = 'export class K { a() { return 1; } }\n';
  const cand = 'export class K { a() { return 1; } get size() { return 3; } }\n';
  const r = stubOf(base, cand, 'k.ts');
  assert.deepEqual(r.symbols, []);
  assert.deepEqual(r.unstubbed, ['K.size']);
  assert.equal(r.text, null);
});

test('a new re-export has no body to stub: it is reported as unstubbed', () => {
  const r = stubOf('export const a = 1;\n', "export const a = 1;\nexport { x } from './other';\n", 'm.ts');
  assert.deepEqual(r.unstubbed, ['x']);
  assert.deepEqual(r.symbols, []);
});

test('a new export whose name the base already binds (not exported) is unstubbed, not redeclared', () => {
  const r = stubOf('function f() { return 1; }\n', 'export function f() { return 2; }\n', 'm.ts');
  assert.deepEqual(r.unstubbed, ['f']);
  assert.equal(r.text, null);
});

test('an async function stub returns a rejected promise', async () => {
  const r = stubOf(null, 'export async function load() { return 1; }\n', 'a.ts');
  const mod = await load(r.text, 'a.mjs');
  await assert.rejects(mod.load(), /a2dlc stub/);
});

test('an export list of a new local function is stubbed under its exported name', async () => {
  const cand = 'function helper(x: number) { return x; }\nexport { helper as publicName };\n';
  const r = stubOf(null, cand, 'e.ts');
  assert.deepEqual(r.symbols, ['publicName']);
  const mod = await load(r.text, 'e.mjs');
  assert.throws(() => mod.publicName(1), /a2dlc stub/);
});

test('an unchanged module gives no stub text', () => {
  const same = 'export function f() { return 1; }\n';
  assert.deepEqual(stubOf(same, same, 'same.ts'), { text: null, symbols: [], unstubbed: [] });
});

test('main writes the stubs and the response; an unparseable candidate is skipped, the others go on', () => {
  const base = tree({ 'ok.ts': 'export const a = 1;\n' });
  const cand = tree({
    'ok.ts': 'export const a = 1;\nexport function added() { return 1; }\n',
    'bad.ts': 'export const = ;\n',
    'notes.md': '# not code\n',
  });
  const io = tree({});
  main({ base_dir: base, candidate_dir: cand, changed_files: ['ok.ts', 'bad.ts', 'notes.md'] }, io);
  const response = JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8'));
  assert.equal(response.error, null);
  assert.deepEqual(response.stubbed, [{ file: 'ok.ts', symbols: ['added'] }]);
  assert.deepEqual(response.unstubbed, ['unparseable: bad.ts']);
  assert.ok(existsSync(path.join(io, 'stubs', 'ok.ts')));
  assert.ok(!existsSync(path.join(io, 'stubs', 'bad.ts')));
});

test('main with a new module names nothing and the response is still schema-shaped', () => {
  const base = tree({});
  const cand = tree({ 'empty.ts': 'import { x } from "y";\n' });
  const io = tree({});
  mkdirSync(path.join(io, 'stubs'), { recursive: true });
  main({ base_dir: base, candidate_dir: cand, changed_files: ['empty.ts'] }, io);
  const response = JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8'));
  assert.deepEqual(Object.keys(response).sort(), ['error', 'schema_version', 'stubbed', 'unstubbed']);
  assert.deepEqual(response.stubbed, []);
});

test('export * from is not listed by name: a new star re-export is unstubbed by its specifier', () => {
  const r = stubOf(null, "export * from './x';\nexport const a = 1;\n", 'm.ts');
  assert.deepEqual(r.unstubbed, ['* from ./x']);
  assert.deepEqual(r.symbols, ['a']);
});

test('an export * already in the base is not new', () => {
  const base = "export * from './x';\nexport const a = 1;\n";
  const cand = `${base}export function added() { return 1; }\n`;
  const r = stubOf(base, cand, 'm.ts');
  assert.deepEqual(r.unstubbed, []);
  assert.deepEqual(r.symbols, ['added']);
});

test('export * as ns from is a re-export: its name is unstubbed', () => {
  const r = stubOf(null, "export * as ns from './x';\n", 'm.ts');
  assert.deepEqual(r.unstubbed, ['ns']);
  assert.equal(r.text, null);
});

test('an unparseable candidate is reported by its file, and the other files are still stubbed', () => {
  const base = tree({ 'u.ts': 'export const ok = 1;\n', 'v.ts': 'export const v = 1;\n' });
  const cand = tree({ 'u.ts': 'export const ok = 1;\nexport const z = 2;\n', 'v.ts': 'export const = ;\n' });
  const io = tree({});
  main({ base_dir: base, candidate_dir: cand, changed_files: ['u.ts', 'v.ts'] }, io);
  const response = JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8'));
  assert.deepEqual(response.unstubbed, ['unparseable: v.ts']);
  assert.deepEqual(response.stubbed, [{ file: 'u.ts', symbols: ['z'] }]);
});
