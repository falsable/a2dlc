// Shared helpers of the engine tests: temporary trees built from literal file maps, and the engine's own directory.
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const ENGINE = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

// A directory with the given files ({ "relative/path.ts": "text" }); {} gives an empty tree.
export function tree(files = {}) {
  const dir = mkdtempSync(path.join(tmpdir(), 'a2dlc-ts-'));
  for (const [rel, text] of Object.entries(files)) {
    const p = path.join(dir, rel);
    mkdirSync(path.dirname(p), { recursive: true });
    writeFileSync(p, text);
  }
  return dir;
}
