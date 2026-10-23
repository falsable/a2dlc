// Fires one AI-DLC v2 sensor through its own dispatcher (bun .claude/tools/aidlc.ts engine sensor fire) over the files
// a2dlc passed in $A2DLC_CHANGED, and writes check-response.schema.json to $A2DLC_OUT. The semantics that differ from
// AI-DLC's advisory default are on purpose: a sensor that could not run (tool-unavailable, script-error) is an error of
// this check (exit 3), never a pass; a file the sensor's own filter rejects is not applicable; a failed sensor is a
// finding bound to the file. Usage inside the image: bun /adapter.ts <sensor id>   (AIDLC_STAGE: build-and-test)
import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";

const sensor = process.argv[2];
if (!sensor) {
	console.error("usage: bun /adapter.ts <sensor id>");
	process.exit(2);
}
const stage = process.env.AIDLC_STAGE ?? "build-and-test";
const files = (process.env.A2DLC_CHANGED ?? "").split(" ").filter(Boolean);
const out = process.env.A2DLC_OUT;
const id = process.env.A2DLC_CHECK ?? `aidlc-${sensor}`;
const project = "/home/aidlc";
const tool = { name: `aidlc-workflows sensor ${sensor}`, version: process.env.AIDLC_REF ?? "306bd83" };
if (!out) {
	console.error("A2DLC_OUT is not set");
	process.exit(2);
}

function detail(p: string | null): string {
	if (!p) return `sensor ${sensor} failed`;
	let body: string;
	try {
		body = readFileSync(`${project}/${p}`, "utf-8");
	} catch {
		return `sensor ${sensor} failed (detail ${p} unreadable)`;
	}
	try {
		const d = JSON.parse(body) as { findings?: unknown[] };
		const msgs = (d.findings ?? []).map((x) => (typeof x === "string" ? x : ((x as { message?: string }).message ?? JSON.stringify(x))));
		if (msgs.length) return msgs.join("; ").slice(0, 500);
	} catch {
		// not JSON: the body itself is the message
	}
	return body.trim().slice(0, 500) || `sensor ${sensor} failed`;
}

const findings: { target: string; message: string }[] = [];
let applicable = 0;
for (const f of files) {
	const r = spawnSync("bun", [".claude/tools/aidlc.ts", "engine", "sensor", "fire", sensor, "--stage", stage, "--output-path", `/cand/${f}`], {
		cwd: project,
		encoding: "utf-8",
	});
	const text = `${r.stdout ?? ""}${r.stderr ?? ""}`;
	if (r.status !== 0) {
		// The sensor's own filter (matches in its manifest) rejects the file: not this sensor's business.
		if (text.includes("does not match sensor")) continue;
		console.error(`dispatcher exited ${r.status} for ${f}: ${text.trim().slice(-300)}`);
		process.exit(4);
	}
	const line = (r.stdout ?? "").trim().split("\n").filter((l) => l.startsWith("{")).pop();
	let v: { result?: string; detail_path?: string | null; note?: string };
	try {
		v = JSON.parse(line ?? "");
	} catch {
		console.error(`no verdict line for ${f}: ${text.slice(-300)}`);
		process.exit(4);
	}
	applicable++;
	if (v.result === "failed") {
		findings.push({ target: `${f}::${sensor}`, message: detail(v.detail_path ?? null) });
	} else if (v.result === "passed" && v.note) {
		console.error(`sensor ${sensor} did not run on ${f}: ${v.note}`);
		process.exit(3);
	} else if (v.result !== "passed") {
		console.error(`unknown result ${String(v.result)} for ${f}`);
		process.exit(4);
	}
}
const resp =
	applicable === 0
		? { schema_version: 1, id, status: "not_applicable", pass: true, findings: [], tool }
		: { schema_version: 1, id, status: "ran", pass: findings.length === 0, findings, tool };
writeFileSync(out, `${JSON.stringify(resp)}\n`);
