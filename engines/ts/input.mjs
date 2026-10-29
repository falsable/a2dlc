// Entry point of the TypeScript engine: routes each operation to its module. Only an absent operation or `diff` reaches
// engine.ts (its sha256 pins the v2 generators measured, so it is not edited). Every other operation goes to its own
// module, and an operation with no module answers in its response schema with an error: it never falls through to a
// diff verdict. A module exports main(req, io); engine.ts and signatures.ts are scripts that run on import. mutate.ts,
// when the image has it, exports runMutate(req, io), which writes the response itself.
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ERROR_BODIES, NO_ERROR_FIELD } from './responses.mjs';

const IO = process.env.A2DLC_IO ?? '/io';
const ENGINE_DIR = path.dirname(fileURLToPath(import.meta.url));
const req = JSON.parse(readFileSync(`${IO}/request.json`, 'utf-8'));
const operation = req.operation ?? 'diff';

const writeResponse = (body) => writeFileSync(`${IO}/response.json`, JSON.stringify(body, null, 1));
const notDone = (what) => `the TypeScript engine does not do ${what} yet`;

// Operations answered by a module of this engine. Each module exports main(req, io).
const MODULES = {
  tier0: './tier0.ts',
  stub: './stub.ts',
  deps_graph: './deps.ts',
};

if (operation === 'diff') {
  await import('./engine.ts');
} else if (operation === 'signatures') {
  await import('./signatures.ts');
} else if (Object.hasOwn(MODULES, operation)) {
  const module = await import(MODULES[operation]);
  await module.main(req, IO);
} else if (operation === 'mutate' && existsSync(path.join(ENGINE_DIR, 'mutate.ts'))) {
  const { runMutate } = await import('./mutate.ts');
  runMutate(req, IO);
} else if (Object.hasOwn(ERROR_BODIES, operation)) {
  writeResponse(ERROR_BODIES[operation](notDone(operation)));
} else if (NO_ERROR_FIELD.has(operation)) {
  process.stderr.write(`${notDone(operation)}; its response schema has no error field\n`);
  process.exitCode = 2;
} else {
  process.stderr.write(`unknown operation: ${JSON.stringify(operation)}\n`);
  process.exitCode = 2;
}
