// a2dlc-aln: an exported function whose text changed but whose code did not (a comment, the layout) is a PASS target with the
// reason "equivalent by AST", carries its own lines, and is not run; any other change keeps being judged. Run through the
// dispatcher, as the image runs it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ENGINE, tree } from './helpers.mjs';

function diff(baseFiles, candFiles, changed = ['m.ts']) {
  const io = tree();
  writeFileSync(path.join(io, 'request.json'), JSON.stringify({
    schema_version: 1, base_ref: 'a'.repeat(40), operation: 'diff', base_dir: tree(baseFiles), candidate_dir: tree(candFiles),
    changed_files: changed, budget: { examples: 30 }, generators_override: null,
  }));
  const r = spawnSync(process.execPath, ['--import', 'tsx', path.join(ENGINE, 'input.mjs')], {
    cwd: ENGINE, env: { ...process.env, A2DLC_IO: io }, encoding: 'utf-8', timeout: 120_000,
  });
  const responsePath = path.join(io, 'response.json');
  assert.ok(existsSync(responsePath), `no response.json (status ${r.status}): ${r.stderr}`);
  const response = JSON.parse(readFileSync(responsePath, 'utf-8'));
  return { response, byName: Object.fromEntries(response.targets.map((t) => [t.target.split('::')[1], t])) };
}

const REASON = /^equivalent by AST/;
// A function the engine cannot judge (it opens a socket, which the sandbox refuses): INCONCLUSIVE if it is ever run.
const remote = (comment, extra = '') => [
  'export const limit = 3;',
  'export async function remote(n: number): Promise<number> {',
  `  ${comment}`,
  "  await fetch('http://example.invalid/a');",
  `  return n${extra};`,
  '}',
  'export const other = 1;',
  '',
].join('\n');

test('a comment inside an exported function the engine cannot judge is a PASS equivalent by AST, never run, with its lines', () => {
  const { response, byName } = diff({ 'm.ts': remote('// keep it simple') }, { 'm.ts': remote('// keep it simple and short') });
  assert.equal(response.error, null);
  assert.deepEqual(Object.keys(byName), ['remote']);
  assert.equal(byName.remote.verdict, 'PASS');
  assert.match(byName.remote.reason, REASON);
  assert.deepEqual(byName.remote.lines, [2, 6]);
  assert.deepEqual(response.out_of_scope, []);
});

test('layout and a JSDoc block are equivalent too; a changed string literal is not', () => {
  const doc = (text) => `/** ${text} */\nexport function f(n: number): number {\n  return n + 1;\n}\n`;
  const { byName: jsdoc } = diff({ 'm.ts': doc('adds one') }, { 'm.ts': doc('adds exactly one') });
  assert.deepEqual(Object.keys(jsdoc), []); // the doc block is leading trivia of the declaration: not part of it
  const spaced = 'export function f(n: number): number {\n  return n   +   1;\n}\n';
  const { byName: layout } = diff({ 'm.ts': doc('adds one') }, { 'm.ts': spaced });
  assert.equal(layout.f.verdict, 'PASS');
  assert.match(layout.f.reason, REASON);
  const str = (s) => `export function g(n: number): string { return '${s}' + n; }\n`;
  const { byName: literal } = diff({ 'm.ts': str('a') }, { 'm.ts': str('b') });
  assert.equal(literal.g.verdict, 'FAIL');
  assert.equal(literal.g.reason, undefined);
});

test('a comment next to a real change is still judged by the engine', () => {
  const { byName } = diff({ 'm.ts': remote('// keep it simple') }, { 'm.ts': remote('// keep it simple and short', ' + 1') });
  assert.equal(byName.remote.verdict, 'INCONCLUSIVE'); // judged (run) and unjudgeable: not "equivalent"
  assert.doesNotMatch(byName.remote.reason, REASON);
});

test('a change only in type annotations is judged, not treated as a comment', () => {
  const sig = (t) => `export function h(n: ${t}): ${t} { return n; }\n`;
  const { byName } = diff({ 'm.ts': sig('number') }, { 'm.ts': sig('bigint') });
  assert.notEqual(byName.h.reason ?? '', 'equivalent by AST');
  assert.doesNotMatch(byName.h.reason ?? '', REASON);
});

test('a changed constant that no exported function reads is not hidden by an equivalent target', () => {
  const { byName } = diff({ 'm.ts': remote('// keep it simple') }, { 'm.ts': remote('// keep it simple and short').replace('limit = 3', 'limit = 4') });
  // `limit` changed on line 1, outside the function's lines [2, 6]: the target must not claim the whole file
  assert.deepEqual(byName.remote.lines, [2, 6]);
});
