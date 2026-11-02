// The import walker: edges run importer to imported; what does not map to a candidate file is unresolved, never dropped.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { depsOf, resolveRelative } from '../deps.ts';
import { tree } from './helpers.mjs';

test('static imports, re-exports, import(), require() and import-equals give edges from the importer', () => {
  const cand = tree({
    'src/app.ts': [
      "import { a } from './calc';",
      "export { b } from './util/b';",
      "export * from './star';",
      "const c = require('./calc.js');",
      "const d = await import('./util/b.mjs');",
      "import e = require('./eq');",
      '',
    ].join('\n'),
    'src/calc.ts': 'export const a = 1;\n',
    'src/util/b.ts': 'export const b = 2;\n',
    'src/star.ts': 'export const s = 3;\n',
    'src/eq.ts': 'export = 4;\n',
    'src/util/b.mts': 'export const bm = 5;\n',
  });
  const r = depsOf(cand, ['src/app.ts', 'src/calc.ts', 'src/eq.ts', 'src/star.ts', 'src/util/b.mts', 'src/util/b.ts']);
  assert.deepEqual(r.edges.map((e) => `${e.from} -> ${e.to}`).sort(), [
    'src/app.ts -> src/calc.ts',
    'src/app.ts -> src/eq.ts',
    'src/app.ts -> src/star.ts',
    'src/app.ts -> src/util/b.mts',
    'src/app.ts -> src/util/b.ts',
  ].sort());
  assert.deepEqual(r.unresolved, []);
});

test('a bare package, a node: builtin and a missing relative file are unresolved; none is dropped', () => {
  const cand = tree({
    'a.ts': "import req from 'requests';\nimport fs from 'node:fs';\nimport { x } from './missing';\nimport { y } from './present';\n",
    'present.ts': 'export const y = 1;\n',
  });
  const r = depsOf(cand, ['a.ts', 'present.ts']);
  assert.deepEqual(r.edges, [{ from: 'a.ts', to: 'present.ts' }]);
  assert.deepEqual(r.unresolved, ['./missing', 'node:fs', 'requests']);
});

test('a non-literal import or require is reported against its file', () => {
  const cand = tree({ 'd.ts': 'export const f = (n: string) => require(n);\nexport const g = (n: string) => import(n);\n' });
  const r = depsOf(cand, ['d.ts']);
  assert.deepEqual(r.edges, []);
  assert.deepEqual(r.unresolved, ['import or require with a non-literal specifier: d.ts']);
});

test('an index file resolves a directory import; an unparseable changed file is reported', () => {
  const cand = tree({
    'lib/index.ts': 'export const i = 1;\n',
    'use.ts': "import { i } from './lib';\n",
    'broken.ts': 'export const = ;\n',
  });
  const r = depsOf(cand, ['use.ts', 'broken.ts', 'lib/index.ts']);
  assert.deepEqual(r.edges, [{ from: 'use.ts', to: 'lib/index.ts' }]);
  assert.deepEqual(r.unresolved, ['unparseable: broken.ts']);
});

test('a relative specifier that climbs out of the candidate tree is unresolved', () => {
  const cand = tree({ 'x/y.ts': "import z from '../../outside';\n" });
  assert.equal(resolveRelative(cand, 'x/y.ts', '../../outside'), null);
  assert.deepEqual(depsOf(cand, ['x/y.ts']).unresolved, ['../../outside']);
});

test('an edge names only requested files: an import of a file nobody asked about gets none', () => {
  const cand = tree({
    'tests/calc.test.ts': "import { a } from '../src/calc';\nimport { h } from '../examples/helper';\n",
    'src/calc.ts': "import { u } from './util';\nexport const a = 1;\n",
    'src/util.ts': 'export const u = 1;\n',
    'examples/helper.ts': "import { a } from '../src/calc';\nexport const h = a;\n",
  });
  const r = depsOf(cand, ['tests/calc.test.ts', 'src/calc.ts', 'src/util.ts']);
  assert.deepEqual(r.edges.map((e) => `${e.from} -> ${e.to}`).sort(), [
    'src/calc.ts -> src/util.ts',
    'tests/calc.test.ts -> src/calc.ts',
  ]);
  assert.deepEqual(r.unresolved, []);
});

test('non-code changed files are not read', () => {
  const cand = tree({ 'README.md': "import x from './nothing';\n", 'c.ts': 'export const c = 1;\n' });
  assert.deepEqual(depsOf(cand, ['README.md', 'c.ts']), { edges: [], unresolved: [] });
});
