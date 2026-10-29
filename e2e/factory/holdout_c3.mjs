// C3 of the software factory calibration (bead a2dlc-3fu.2): the own holdout against reference/, untouched tinylinks/
// and every frozen mutant, read case by case from vitest's JUnit reporter. Runs inside a2dlc-factory-node:0.1.
// The unit of "a holdout test" is the file of one issue (h01..h09), as score.sh scores it. C3 holds when:
//   - reference/ passes every case, the same in every run;
//   - untouched tinylinks/ fails every file, by assertions (it reports the same cases as reference/), in every run;
//   - every mutant fails its target file by assertions, with the same cases reported as reference/;
//   - every case is failed by at least one mutant that targets its issue (collateral kills do not count).
// A mutant is one exact replacement on a copy of reference/ (holdout mutants.json); the base files are checked by sha256.
// Usage: node holdout_c3.mjs <reference-dir> <tinylinks-dir> <holdout-dir> [runs, default 3]
// Prints the kill matrix; exit 0 when C3 holds, 1 when it does not, 2 for a broken setup.
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [refDir, tinyDir, holdoutDir, runsArg] = process.argv.slice(2);
if (!refDir || !tinyDir || !holdoutDir) {
  console.error("usage: node holdout_c3.mjs <reference-dir> <tinylinks-dir> <holdout-dir> [runs]");
  process.exit(2);
}
const RUNS = Number(runsArg ?? 3);
const NODE_MODULES = process.env.NODE_MODULES ?? "/deps/node_modules";
const testsDir = join(holdoutDir, "tests");
const files = readdirSync(testsDir).filter((f) => /^h\d\d\.test\.ts$/.test(f)).sort();
if (files.length !== 9) setup(`expected h01..h09 in ${testsDir}, found ${files.join(" ")}`);
const spec = JSON.parse(readFileSync(join(holdoutDir, "mutants.json"), "utf8"));

function setup(msg) {
  console.error(`SETUP: ${msg}`);
  process.exit(2);
}
const sha256 = (s) => createHash("sha256").update(s).digest("hex");
const unxml = (s) => s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, "&");

/** Runs the holdout on a copy of tree, after one mutant if given. Returns Map "hNN > case name" -> "PASS" | "FAIL". */
function run(tree, mutant) {
  const w = mkdtempSync(join(tmpdir(), "c3-"));
  try {
    cpSync(tree, w, { recursive: true, filter: (p) => !/(^|[\\/])(node_modules|\.git)$/.test(p) });
    symlinkSync(NODE_MODULES, join(w, "node_modules"));
    if (mutant) {
      const target = join(w, mutant.file);
      const before = readFileSync(target, "utf8");
      if (sha256(before) !== spec.base_sha256[mutant.file]) setup(`${mutant.id}: ${mutant.file} is not the frozen base`);
      const n = before.split(mutant.old).length - 1;
      if (n !== 1) setup(`${mutant.id}: 'old' occurs ${n} times in ${mutant.file}, not once`);
      writeFileSync(target, before.replace(mutant.old, () => mutant.new));
    }
    const hidden = join(w, "test", "_holdout");
    mkdirSync(hidden, { recursive: true });
    // Same import rewrite as score.sh: helpers.ts sits one folder deeper than the repo's own test/.
    writeFileSync(join(hidden, "helpers.ts"), readFileSync(join(testsDir, "helpers.ts"), "utf8").replace("../src/", "../../src/"));
    for (const f of files) cpSync(join(testsDir, f), join(hidden, f));
    const xml = join(w, "junit.xml");
    const r = spawnSync(join(w, "node_modules", ".bin", "vitest"), ["run", "test/_holdout", "--reporter=junit", `--outputFile.junit=${xml}`],
      { cwd: w, encoding: "utf8", timeout: 180_000 });
    let text;
    try { text = readFileSync(xml, "utf8"); } catch { setup(`no JUnit from ${tree}${mutant ? " + " + mutant.id : ""}: exit ${r.status}\n${(r.stdout + r.stderr).slice(-1500)}`); }
    const cases = new Map();
    for (const m of text.matchAll(/<testcase\b([^>]*?)(\/>|>([\s\S]*?)<\/testcase>)/g)) {
      const attr = (k) => unxml((m[1].match(new RegExp(`\\b${k}="([^"]*)"`)) ?? [])[1] ?? "");
      const file = (attr("classname").match(/(h\d\d)\.test\.ts/) ?? [])[1] ?? attr("classname");
      const name = attr("name").split(" > ").pop();
      cases.set(`${file} > ${name}`, /<(failure|error)\b/.test(m[3] ?? "") ? "FAIL" : "PASS");
    }
    return cases;
  } finally {
    rmSync(w, { recursive: true, force: true });
  }
}

const problems = [];
const same = (a, b) => a.size === b.size && [...a].every(([k, v]) => b.get(k) === v);
const fileOf = (key) => key.slice(0, 3);
const failedFiles = (cases) => new Set([...cases].filter(([, v]) => v === "FAIL").map(([k]) => fileOf(k)));

const ref = run(refDir);
const want = [...ref.keys()];
console.log(`reference/: ${want.length} cases in ${new Set(want.map(fileOf)).size} files`);
for (let i = 2; i <= RUNS; i++) if (!same(ref, run(refDir))) problems.push(`reference/ run ${i} differs from run 1`);
const bad = [...ref].filter(([, v]) => v !== "PASS").map(([k]) => k);
if (bad.length) problems.push(`reference/ fails: ${bad.join("; ")}`);
if (new Set(want.map(fileOf)).size !== 9) problems.push("reference/ did not report cases for all nine files");

const sameCases = (cases) => cases.size === ref.size && want.every((k) => cases.has(k));
const tiny = run(tinyDir);
for (let i = 2; i <= RUNS; i++) if (!same(tiny, run(tinyDir))) problems.push(`tinylinks/ run ${i} differs from run 1`);
if (!sameCases(tiny)) problems.push("tinylinks/ did not report the same cases as reference/ (a load error, not an assertion)");
const tinyFailed = failedFiles(tiny);
const tinyPassing = files.map((f) => f.slice(0, 3)).filter((f) => !tinyFailed.has(f));
if (tinyPassing.length) problems.push(`untouched tinylinks/ passes ${tinyPassing.join(" ")}`);
console.log(`tinylinks/: fails ${[...tinyFailed].sort().join(" ")}; ${[...tiny.values()].filter((v) => v === "FAIL").length}/${tiny.size} cases fail`);

const killedBy = new Map(want.map((k) => [k, []]));
console.log("\nmutant                    target  target cases failed  collateral files");
for (const m of spec.mutants) {
  const cases = run(refDir, m);
  const target = `h${String(m.issue).padStart(2, "0")}`;
  if (!sameCases(cases)) { problems.push(`${m.id}: did not report the same cases as reference/`); continue; }
  const hits = want.filter((k) => fileOf(k) === target && cases.get(k) === "FAIL");
  hits.forEach((k) => killedBy.get(k).push(m.id));
  const collateral = [...failedFiles(cases)].filter((f) => f !== target).sort();
  if (!hits.length) problems.push(`${m.id}: its target ${target} passes (the mutant survives)`);
  const total = want.filter((k) => fileOf(k) === target).length;
  console.log(`${m.id.padEnd(26)}${target.padEnd(8)}${`${hits.length}/${total}`.padEnd(21)}${collateral.join(" ") || "-"}`);
}

console.log("\ncase -> mutants of its issue that fail it");
for (const [k, ms] of killedBy) {
  console.log(`${k}: ${ms.join(", ") || "NONE"}`);
  if (!ms.length) problems.push(`no mutant of its issue fails '${k}'`);
}
const perIssue = files.map((f) => `${f.slice(0, 3)}:${spec.mutants.filter((m) => `h${String(m.issue).padStart(2, "0")}` === f.slice(0, 3)).length}`);
console.log(`\n${spec.mutants.length} mutants (${perIssue.join(" ")}), ${want.length} cases, ${RUNS} runs of reference/ and tinylinks/`);
if (problems.length) {
  console.log("C3 FAIL:\n  " + problems.join("\n  "));
  process.exit(1);
}
console.log("C3 PASS");
