// Every error body the dispatcher writes is in the shape of its response schema: the required fields are present, no field
// is outside the schema, and each value has the schema's type. The schemas are the repo's contracts/ folder, which run.sh
// mounts and names in A2DLC_CONTRACTS; the test fails when that is missing rather than skipping the check.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { ERROR_BODIES } from '../responses.mjs';
import { main } from '../classify.ts';
import { tree } from './helpers.mjs';

const CONTRACTS = process.env.A2DLC_CONTRACTS;

// The response schema of each operation in ERROR_BODIES (golden_* share one, capture_traces writes the traces schema).
const SCHEMA_OF = {
  mutate: 'mutate', complexity: 'complexity', lemmas: 'lemmas', golden_record: 'golden', golden_replay: 'golden',
  capture_traces: 'traces', classify_lines: 'classify-lines',
};

function schemaOf(operation) {
  assert.ok(CONTRACTS, 'A2DLC_CONTRACTS must name the contracts/ folder (tests/run.sh mounts it)');
  const file = path.join(CONTRACTS, `engine-${SCHEMA_OF[operation]}-response.schema.json`);
  const schema = JSON.parse(readFileSync(file, 'utf-8'));
  assert.equal(schema.additionalProperties, false, `${file} must be closed`);
  return schema;
}

const typeOf = (v) => (v === null ? 'null' : Array.isArray(v) ? 'array' : Number.isInteger(v) ? 'integer' : typeof v);

// The schema's own checks this body can fail: required, closed properties, a JSON type, and an enum.
function shapeErrors(body, schema) {
  const errors = [];
  for (const key of schema.required) if (!(key in body)) errors.push(`missing ${key}`);
  for (const [key, value] of Object.entries(body)) {
    const prop = schema.properties[key];
    if (!prop) {
      errors.push(`field outside the schema: ${key}`);
      continue;
    }
    if (prop.type !== undefined) {
      const types = [prop.type].flat();
      const ok = types.includes(typeOf(value)) || (typeOf(value) === 'integer' && types.includes('number'));
      if (!ok) errors.push(`${key} is ${typeOf(value)}, the schema says ${types.join(' or ')}`);
    }
    if (prop.enum !== undefined && !prop.enum.includes(value)) errors.push(`${key} is outside its enum`);
  }
  return errors;
}

for (const operation of Object.keys(ERROR_BODIES)) {
  test(`the ${operation} error body is in the shape of its response schema`, () => {
    const body = ERROR_BODIES[operation]('a reason');
    assert.deepEqual(shapeErrors(body, schemaOf(operation)), []);
    assert.equal(body.schema_version, 1);
    assert.equal(typeof body.error, 'string');
  });
}

test('what classify_lines writes, with notes and without, is in the shape of its response schema', () => {
  const cand = tree({ 'a.ts': "type P = 'a';\nexport const x = 1;\n", 'b.js': 'var b;\n' });
  const io = tree({});
  main({ changed_files: ['a.ts', 'b.js'], candidate_dir: cand, budget: { lines: { 'a.ts': [1, 2], 'b.js': [1] } } }, io);
  const body = JSON.parse(readFileSync(path.join(io, 'response.json'), 'utf-8'));
  const schema = schemaOf('classify_lines');
  assert.deepEqual(shapeErrors(body, schema), []);
  assert.deepEqual(body.files.map((f) => f.notes.length), [1, 0]);
  assert.deepEqual(shapeErrors(body.files[0].notes[0], schema.properties.files.items.properties.notes.items), []);
});

test('the shape check itself catches a missing required field and an extra field', () => {
  const schema = { additionalProperties: false, required: ['a', 'b'], properties: { a: { type: 'array' }, b: { type: ['string', 'null'] } } };
  assert.deepEqual(shapeErrors({ a: [], b: null }, schema), []);
  assert.deepEqual(shapeErrors({ a: [] }, schema), ['missing b']);
  assert.deepEqual(shapeErrors({ a: [], b: null, c: 1 }, schema), ['field outside the schema: c']);
  assert.deepEqual(shapeErrors({ a: {}, b: null }, schema), ['a is object, the schema says array']);
});
