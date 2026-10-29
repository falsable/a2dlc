// The dispatcher runs as the image runs it: input.mjs under tsx, with A2DLC_IO pointing at a temporary folder.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ENGINE, tree } from './helpers.mjs';

function run(request, extraFiles = {}) {
  const io = tree(extraFiles);
  writeFileSync(path.join(io, 'request.json'), JSON.stringify(request));
  const r = spawnSync(process.execPath, ['--import', 'tsx', path.join(ENGINE, 'input.mjs')], {
    cwd: ENGINE, env: { ...process.env, A2DLC_IO: io }, encoding: 'utf-8', timeout: 120_000,
  });
  const responsePath = path.join(io, 'response.json');
  const response = existsSync(responsePath) ? JSON.parse(readFileSync(responsePath, 'utf-8')) : null;
  return { status: r.status, response, stderr: r.stderr };
}

const base = { schema_version: 1, base_ref: 'a'.repeat(40), base_dir: '/base', candidate_dir: '/cand', generators_override: null };

test('tier0 is routed to the Tier 0 module and answers in its shape', () => {
  const candDir = tree({ 'a.ts': 'export const x: number = "bad";\n' });
  const baseDir = tree({});
  const out = run({ ...base, operation: 'tier0', base_dir: baseDir, candidate_dir: candDir, changed_files: ['a.ts'], budget: {} });
  assert.equal(out.status, 0, out.stderr);
  assert.equal(out.response.ok, false);
  assert.equal(out.response.findings[0].kind, 'typecheck');
  assert.equal(out.response.error, null);
});

test('stub is routed to the stub module', () => {
  const baseDir = tree({ 'm.ts': 'export const a = 1;\n' });
  const candDir = tree({ 'm.ts': 'export const a = 1;\nexport function added() { return 1; }\n' });
  const out = run({ ...base, operation: 'stub', base_dir: baseDir, candidate_dir: candDir, changed_files: ['m.ts'] });
  assert.equal(out.status, 0, out.stderr);
  assert.deepEqual(out.response.stubbed, [{ file: 'm.ts', symbols: ['added'] }]);
  assert.equal(out.response.error, null);
});

test('mutate routes to mutate module when present or fallback error when absent', () => {
  const out = run({ ...base, operation: 'mutate', changed_files: ['a.ts'], budget: { changed_lines: { 'a.ts': [1] }, max_mutants: 5 } });
  assert.equal(out.status, 0, out.stderr);
  assert.ok(Array.isArray(out.response.mutants));
  assert.ok(Array.isArray(out.response.not_generated));
  assert.ok(Array.isArray(out.response.pruned));
  if (out.response.error !== null) {
    assert.match(out.response.error, /does not do mutate yet/);
  }
});

test('complexity and the operations whose schema has an error field answer with that error', () => {
  const complexity = run({ ...base, operation: 'complexity', changed_files: [] });
  assert.match(complexity.response.error, /does not do complexity yet/);
  assert.deepEqual(complexity.response.files, []);
  const lemmas = run({ ...base, operation: 'lemmas', changed_files: [], budget: {} });
  assert.equal(lemmas.status, 0, lemmas.stderr);
  assert.match(lemmas.response.error, /does not do lemmas yet/);
  assert.deepEqual(lemmas.response.lemmas, []);
  for (const operation of ['golden_record', 'golden_replay']) {
    const out = run({ ...base, operation, changed_files: [], budget: { cases: ['c1'] } });
    assert.equal(out.status, 0, out.stderr);
    assert.match(out.response.error, new RegExp(`does not do ${operation} yet`));
    assert.deepEqual(out.response.diffs, []);
  }
  const traces = run({ ...base, operation: 'capture_traces', changed_files: [], budget: {} });
  assert.equal(traces.status, 0, traces.stderr);
  assert.match(traces.response.error, /does not do capture_traces yet/);
  assert.deepEqual(traces.response.traces, []);
});

test('fuzz has no error field in its schema: it writes no response and exits 2, never an empty pass', () => {
  const out = run({ ...base, operation: 'fuzz', changed_files: [], budget: { seconds: 1, seed: 1 } });
  assert.equal(out.status, 2);
  assert.equal(out.response, null);
  assert.match(out.stderr, /does not do fuzz yet/);
});

test('deps_graph is served by the import walker: no changed source file gives no edges', () => {
  const out = run({ ...base, operation: 'deps_graph', changed_files: [], budget: {} });
  assert.equal(out.status, 0, out.stderr);
  assert.deepEqual(out.response, { schema_version: 1, edges: [], unresolved: [], error: null });
});

test('an unknown operation exits 2 and never reaches the diff engine', () => {
  const out = run({ ...base, operation: 'bogus', changed_files: [] });
  assert.equal(out.status, 2);
  assert.equal(out.response, null);
  assert.match(out.stderr, /unknown operation/);
});

test('an absent operation is the diff engine: with no changed files it answers with no targets', () => {
  const out = run({ ...base, changed_files: [], budget: {}, generators_override: null });
  assert.equal(out.status, 0, out.stderr);
  assert.deepEqual(out.response.targets, []);
  assert.deepEqual(out.response.out_of_scope, []);
  assert.equal(out.response.error, null);
});
