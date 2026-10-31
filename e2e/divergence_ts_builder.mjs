// The builder of e2e/divergence_ts.sh: no model. Round 1 rewrites src/lib/label.ts so that label(0) changes. Round 2 must
// have received the divergence finding of the core (the function, the input, old and new, restore or pin); it changes
// nothing and answers ok, or refuses with what was missing, so a run that never delivered the feedback cannot pass.
import fs from 'node:fs';

const req = JSON.parse(fs.readFileSync(process.env.A2DLC_REQUEST, 'utf8'));
const pl = req.payload;
const answer = (status, payload, reason) => fs.writeFileSync(process.env.A2DLC_RESPONSE, JSON.stringify({
  schema_version: 1, run_id: req.run_id, role: req.role, stage: req.stage, attempt: req.attempt, status,
  ...(reason ? { reason } : {}), cost: { usd: 0.01 }, payload,
}));

if (pl.round === 1) {
  if (pl.tier0_feedback !== null) {
    answer('refused', {}, 'round 1 must not have feedback');
  } else {
    const file = 'src/lib/label.ts';
    fs.writeFileSync(file, fs.readFileSync(file, 'utf8').replace('"zero"', '"none"'));
    answer('ok', { summary: 'label(0) now says none', files_changed: [file] });
  }
} else {
  const f = (pl.tier0_feedback?.findings ?? []).find((x) => x.kind === 'divergence');
  const need = ['src/lib/label.ts::label', 'zero', 'none', 'restore the old behavior', 'pins the new behavior'];
  const missing = f ? need.filter((s) => !f.message.includes(s)) : need;
  if (!f || missing.length) {
    answer('refused', {}, `round ${pl.round}: no divergence finding with ${JSON.stringify(missing)}: ${JSON.stringify(pl.tier0_feedback)}`);
  } else {
    answer('ok', { summary: 'told about the divergence, kept it', files_changed: ['src/lib/label.ts'] });
  }
}
