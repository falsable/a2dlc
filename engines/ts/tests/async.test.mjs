// The diff operation on modules with async code: each exported function is its own target, a promise is awaited, and
// only a side effect the engine cannot sandbox (network, filesystem write, timers), a result that is not data, or a call
// that never settles makes a target INCONCLUSIVE. Run through the dispatcher, as the image runs it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ENGINE, tree } from './helpers.mjs';

function diff(baseFiles, candFiles, changed, budget = { examples: 30 }) {
  const io = tree();
  writeFileSync(path.join(io, 'request.json'), JSON.stringify({
    schema_version: 1, base_ref: 'a'.repeat(40), operation: 'diff', base_dir: tree(baseFiles), candidate_dir: tree(candFiles),
    changed_files: changed, budget, generators_override: null,
  }));
  const r = spawnSync(process.execPath, ['--import', 'tsx', path.join(ENGINE, 'input.mjs')], {
    cwd: ENGINE, env: { ...process.env, A2DLC_IO: io }, encoding: 'utf-8', timeout: 120_000,
  });
  const responsePath = path.join(io, 'response.json');
  assert.ok(existsSync(responsePath), `no response.json (status ${r.status}): ${r.stderr}`);
  const response = JSON.parse(readFileSync(responsePath, 'utf-8'));
  const byName = Object.fromEntries(response.targets.map((t) => [t.target.split('::')[1], t]));
  return { status: r.status, response, byName };
}

test('one module, one status per exported function: sync, async, and async that touches the network', () => {
  const base = { 'm.ts': [
    'export function pure(n: number): number { return n + 1; }',
    'export async function pureAsync(n: number): Promise<number> { await Promise.resolve(); return n * 2; }',
    'export async function same(n: number): Promise<number> { await Promise.resolve(); return n * 2; }',
    "export async function remote(n: number): Promise<number> { await fetch('http://example.invalid/a'); return n; }",
    '',
  ].join('\n') };
  const cand = { 'm.ts': [
    'export function pure(n: number): number { return n + 2; }',
    'export async function pureAsync(n: number): Promise<number> { await Promise.resolve(); return n * 3; }',
    'export async function same(n: number): Promise<number> { await Promise.resolve(); return n + n; }',
    "export async function remote(n: number): Promise<number> { await fetch('http://example.invalid/b'); return n; }",
    '',
  ].join('\n') };
  const { status, response, byName } = diff(base, cand, ['m.ts']);
  assert.equal(status, 0);
  assert.equal(response.error, null);
  assert.equal(byName.pure.verdict, 'FAIL');
  assert.equal(byName.pureAsync.verdict, 'FAIL', 'an async function is awaited and compared by what it resolves to');
  assert.deepEqual(Object.keys(byName.pureAsync.counterexample).sort(), ['args', 'new', 'old']);
  assert.match(byName.pureAsync.counterexample.old, /"ok"/);
  assert.equal(byName.same.verdict, 'PASS', 'an equivalent async rewrite is a PASS');
  assert.equal(byName.remote.verdict, 'INCONCLUSIVE');
  assert.match(byName.remote.reason, /network \(fetch\)/);
  assert.doesNotMatch(Object.values(byName).map((t) => t.reason ?? '').join('\n'), /not supported yet/);
});

test('a rejected promise is a result: two versions that reject the same way agree, and one that stops rejecting differs', () => {
  const base = { 'r.ts': [
    'export async function bad(n: number): Promise<number> { if (n >= 0) throw new Error("boom"); return n; }',
    'export async function drift(n: number): Promise<number> { if (n === 3) throw new Error("boom"); return n; }',
    '',
  ].join('\n') };
  const cand = { 'r.ts': [
    'export async function bad(n: number): Promise<number> { if (n >= 0) { throw new Error("boom"); } return n; }',
    'export async function drift(n: number): Promise<number> { if (n === 4) throw new Error("boom"); return n; }',
    '',
  ].join('\n') };
  const { byName } = diff(base, cand, ['r.ts'], { examples: 500 });
  assert.equal(byName.bad.verdict, 'PASS');
  assert.equal(byName.drift.verdict, 'FAIL');
  assert.match(byName.drift.counterexample.old + byName.drift.counterexample.new, /lanza/);
});

test('an async function whose every call raises is INCONCLUSIVE: nothing was compared', () => {
  const mk = (tag) => ({ 'x.ts': `export async function never(n: number): Promise<number> { // ${tag}\n  throw new Error('always');\n}\n` });
  const { byName } = diff(mk('a'), mk('b'), ['x.ts']);
  assert.equal(byName.never.verdict, 'INCONCLUSIVE');
  assert.match(byName.never.reason, /every one raised/);
});

test('filesystem writes and timers are INCONCLUSIVE with their name; reads and plain promises are not', () => {
  const body = (extra) => [
    "import { writeFileSync, readFileSync } from 'node:fs';",
    `export function writer(n: number): number { writeFileSync('/tmp/a2dlc-ts-test-${extra}', String(n)); return n; }`,
    `export async function sleeper(n: number): Promise<number> { await new Promise((r) => setTimeout(r, 1)); return n + ${extra.length}; }`,
    `export function reader(n: number): number { return readFileSync('/proc/self/stat', 'utf-8').length > 0 ? n + ${extra.length} : 0; }`,
    '',
  ].join('\n');
  const { byName } = diff({ 'f.ts': body('a') }, { 'f.ts': body('bb') }, ['f.ts']);
  assert.equal(byName.writer.verdict, 'INCONCLUSIVE');
  assert.match(byName.writer.reason, /filesystem write \(writeFileSync\)/);
  assert.equal(byName.sleeper.verdict, 'INCONCLUSIVE');
  assert.match(byName.sleeper.reason, /timers \(setTimeout\)/);
  assert.equal(byName.reader.verdict, 'FAIL', 'reading is not an effect: the divergence is found');
});

test('a call that never settles is INCONCLUSIVE with the time limit, and the engine still exits', () => {
  const mk = (k) => ({ 'h.ts': `export async function hang(n: number): Promise<number> { await new Promise<void>(() => {}); return n + ${k}; }\n` });
  const t0 = Date.now();
  const { status, byName } = diff(mk(1), mk(2), ['h.ts'], { examples: 5 });
  assert.equal(status, 0);
  assert.equal(byName.hang.verdict, 'INCONCLUSIVE');
  assert.match(byName.hang.reason, /did not finish within 1 s/);
  assert.ok(Date.now() - t0 < 30_000, 'a hanging promise must not keep the process alive');
});

test('a function that returns a server is INCONCLUSIVE for network, one that returns a function for not being data', () => {
  const mk = (k) => ({ 's.ts': [
    "import { createServer } from 'node:http';",
    `export function mkServer(n: number) { return createServer(() => { /* ${k} */ }); }`,
    `export function mkFn(n: number) { return () => n + ${k.length}; }`,
    '',
  ].join('\n') });
  const { byName } = diff(mk('a'), mk('bb'), ['s.ts']);
  assert.equal(byName.mkServer.verdict, 'INCONCLUSIVE');
  assert.match(byName.mkServer.reason, /network/);
  assert.equal(byName.mkFn.verdict, 'INCONCLUSIVE');
  assert.match(byName.mkFn.reason, /contains a function/);
});

test('a module that opens a socket when it loads is INCONCLUSIVE for every target in it, with the effect named', () => {
  const mk = (k) => ({ 'l.ts': [
    "import { createServer } from 'node:http';",
    "createServer(() => {}).listen(0);",
    `export function f(n: number): number { return n + ${k}; }`,
    '',
  ].join('\n') });
  const { byName } = diff(mk(1), mk(2), ['l.ts']);
  assert.equal(byName.f.verdict, 'INCONCLUSIVE');
  assert.match(byName.f.reason, /network \(server listen\)/);
});

test('a module the step adds is out of scope as "added" and does not stop the changed ones from being judged', () => {
  const base = { 'src/server.ts': 'export function pick(n: number): number { return n + 1; }\n' };
  const cand = {
    'src/server.ts': "import { limit } from './rateLimit.js';\nexport function pick(n: number): number { return n + limit(); }\n",
    'src/rateLimit.ts': 'export function limit(): number { return 1; }\n',
  };
  const { status, response, byName } = diff(base, cand, ['src/rateLimit.ts', 'src/server.ts']);
  assert.equal(status, 0);
  assert.deepEqual(response.out_of_scope, [{ file: 'src/rateLimit.ts', reason: 'added' }]);
  assert.equal(byName.pick.verdict, 'PASS', 'the candidate imports the added module and behaves as the base');
});
