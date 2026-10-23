// aidlc-sensor-a2dlc.ts — AI-DLC v2 sensor script for the `a2dlc` sensor. The dispatcher runs it as
//   bun .claude/tools/aidlc-sensor-a2dlc.ts --stage <slug> --output-path <deliverable>
// and reads one JSON line {"pass", "findings", "findings_count"} from stdout. It runs `a2dlc verify` on the project
// (the receipt check: no engines, no network) and passes only on a PASS verdict bound to the committed tree. It exits
// 0 whatever it finds, as the sensor contract asks. A2DLC_BIN overrides the binary (default: a2dlc on PATH),
// A2DLC_COMMIT the commit to verify (default HEAD).
import { spawnSync } from "node:child_process";

const bin = process.env.A2DLC_BIN ?? "a2dlc";
const commit = process.env.A2DLC_COMMIT ?? "HEAD";
const r = spawnSync(bin, ["verify", "--commit", commit], { encoding: "utf-8" });
let line: string;
let pass: boolean;
if (r.error) {
	pass = false;
	line = `a2dlc binary not found or not runnable: ${bin} (${r.error.message})`;
} else {
	pass = r.status === 0;
	line = `${r.stdout ?? ""}${r.stderr ?? ""}`.replace(/\s+/g, " ").trim().slice(0, 400);
}
const findings = pass ? [] : [line];
console.log(JSON.stringify({ pass, findings, findings_count: findings.length, verify: line, exit: r.status }));
