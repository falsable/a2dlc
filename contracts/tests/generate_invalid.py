"""Generates the invalid examples from the valid ones: each one breaks a rule of the contract.
Usage: python contracts/tests/generate_invalid.py (from the repo root)."""
import copy
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "examples"
VALID, INVALID = ROOT / "valid", ROOT / "invalid"


def read(name):
    return json.loads((VALID / name).read_text(encoding="utf-8"))


def write(name, doc):
    # newline="\n": on Windows write_text would write CRLF and the tree would get dirty (.gitattributes requires LF)
    (INVALID / name).write_text(json.dumps(doc, ensure_ascii=False, indent=1) + "\n", encoding="utf-8", newline="\n")


INVALID.mkdir(exist_ok=True)
r = read("engine-response__ts-fail.json")
verdict = read("verdict__pending.json")
w = read("waivers__entropos.json")
a = read("approvals__signed.json")
verdict_engine_down = read("verdict__engine-down.json")
c = read("config__entropos.json")
b = read("blackbox__a2dlc.json")
ct = read("config__tests-py.json")
rs = read("engine-request__py-stub.json")
st = read("engine-stub-response__py.json")
verdict_tests = read("verdict__tests.json")
escalation = read("escalation__check.json")
ro = read("roles__claude-gemini.json")
rq = read("role-request__saboteur.json")
rr = read("role-response__saboteur-ok.json")
rs_ = read("run-state__stage4.json")
lg = read("run-ledger__two-entries.json")
sg = read("engine-signatures-response__py.json")

cases = {
    # search_budget of CrossHair in a fast-check target (protocol §6.4: schema error)
    "engine-response__budget-from-another-engine.json": (r, lambda x: x["targets"][0].update(search_budget={"iterations": 5, "per_condition_timeout_s": 10})),
    "engine-response__fail-missing-counterexample.json": (r, lambda x: x["targets"][0].update(counterexample=None)),
    "engine-response__inconclusive-missing-reason.json": (r, lambda x: x["targets"][0].update(verdict="INCONCLUSIVE")),
    # the engine knows nothing of waivers: the core checks that
    "engine-response__waived-in-the-engine.json": (r, lambda x: x["targets"][0].update(waived=True)),
    # the pilot emitted WAIVED as a verdict value
    "verdict__value-waived-of-the-pilot.json": (verdict, lambda x: x["targets"][0].update(verdict="WAIVED")),
    # the pilot used a truncated sha256 of the bytes; the contract asks for the git tree
    "verdict__tree-hash-of-the-pilot.json": (verdict, lambda x: x.update(tree_hash="sha256:a1ba0bf601f193c0")),
    "verdict__missing-waived.json": (verdict, lambda x: x["targets"][0].pop("waived")),
    "verdict__extra-field.json": (verdict, lambda x: x.update(targets_run=1)),
    # the pilot used a flat list with ids and no file
    "waivers__target-missing-file.json": (w, lambda x: x["waivers"][0].update(target="tabToPath")),
    "waivers__decision-unknown.json": (w, lambda x: x["waivers"][0].update(decision="ignore")),
    # approved_by written by the agent was the pilot's approval: no longer valid (an approval is a signature)
    "waivers__approved-by-of-the-pilot.json": (w, lambda x: x["waivers"][0].update(approved_by="aldemar")),
    # without file_blob the signature does not bind the approval to the code: it would be valid for any future change
    "approvals__missing-file-blob.json": (a, lambda x: x["approvals"][0].pop("file_blob")),
    "approvals__signature-no-ssh.json": (a, lambda x: x["approvals"][0].update(signature="aldemar")),
    "verdict__error-of-engine-missing-files.json": (verdict_engine_down, lambda x: x["engine_errors"][0].update(files=[])),
    # a cap of 0 would make check never run engines
    "config__max-files-zero.json": (c, lambda x: x.update(max_files=0)),
    # the surface does not go in budget: the budget is {examples, seed}, the contract's search_budget
    "blackbox__missing-build.json": (b, lambda x: x.pop("build")),
    # http does not exist yet: a v0.1 engine would leave it INCONCLUSIVE, the schema rejects it first
    "blackbox__kind-http.json": (b, lambda x: x["surfaces"][0].update(kind="http")),
    "blackbox__compare-unknown.json": (b, lambda x: x["surfaces"][0]["compare"].update(time=True)),
    # without command there is no way to run the new tests: the NOP gate does not exist
    "config__tests-missing-command.json": (ct, lambda x: x["tests"].pop("command")),
    # k = 0 would declare flaky without rerunning anything
    "config__tests-reruns-zero.json": (ct, lambda x: x["tests"].update(reruns=0)),
    # «format» is neither an escalation nor will it be an engine operation (mutate stopped being unknown in block 5)
    "engine-request__operation-unknown.json": (rs, lambda x: x.update(operation="format")),
    # without unstubbed, a failure caused by a symbol without a stub would pass as a valid failure
    "engine-stub-response__missing-unstubbed.json": (st, lambda x: x.pop("unstubbed")),
    "verdict__test-fail-missing-reason.json": (verdict_tests, lambda x: x["tests"][1].pop("reason")),
    "verdict__test-status-unknown.json": (verdict_tests, lambda x: x["tests"][0].update(base="xfail")),
    # a gate that did not run must say why
    "verdict__gate-error-missing-reason.json": (verdict_tests, lambda x: x["test_gates"]["baseline"].update(status="error")),
    "verdict__gates-missing-baseline.json": (verdict_tests, lambda x: x["test_gates"].pop("baseline")),
    "escalation__kind-unknown.json": (escalation, lambda x: x["items"][0].update(kind="cb-01")),
    "escalation__resolve-with-unknown.json": (escalation, lambda x: x["items"][1].update(resolve_with=["ignore"])),
    "escalation__id-free.json": (escalation, lambda x: x["items"][0].update(id="div-07")),
    # the role that brings the tests of the escalation is one of the protocol's; «builder» does not resolve a missing coverage
    "escalation__needs-role-unknown.json": (escalation, lambda x: x["items"][5].update(needs={"role": "builder", "artifact": "test_files"})),
    "escalation__trees-state-unknown.json": (escalation, lambda x: x["items"][4]["evidence"]["trees"].update(candidate="xfail")),
    # line 0 does not exist: coverage counts from 1
    "verdict__coverage-line-zero.json": (verdict_tests, lambda x: x["coverage"]["files"][0].update(uncovered=[0, 21])),
    "verdict__coverage-missing-status.json": (verdict_tests, lambda x: x["coverage"].pop("status")),
    "verdict__gate-unknown.json": (verdict_tests, lambda x: x["tests"][3].update(gate="n_tree")),
    # escalation lines [first, last]: a single number does not say what the target examined
    "engine-response__lines-only-one.json": (read("engine-response__py-crosshair.json"), lambda x: x["targets"][0].update(lines=[12])),
    # a2dlc run (ADR 0004, amendment 2026-10-17). The oracle is not a role: the core runs it
    "roles__role-unknown.json": (ro, lambda x: x["roles"].update(oracle=x["roles"]["saboteur"])),
    # without argv there is no command to launch; the mount policy is not configured here
    "roles__missing-argv.json": (ro, lambda x: x["roles"]["saboteur"].pop("argv")),
    "roles__mounts-in-config.json": (ro, lambda x: x["roles"]["saboteur"].update(mounts=["/repo:/work"])),
    # the saboteur never has a workdir: it does not see the builder's code
    "role-request__saboteur-with-workdir.json": (rq, lambda x: x.update(workdir="/work")),
    # an input without a hash cannot be attested in the ledger
    "role-request__input-missing-hash.json": (rq, lambda x: x["inputs"][0].pop("sha256")),
    "role-response__refused-missing-reason.json": (rr, lambda x: x.update(status="refused")),
    # the cost is what the CLI reported: without usd there is no report
    "role-response__cost-missing-usd.json": (rr, lambda x: x.update(cost={"input_tokens": 10})),
    # a done stage has to leave an artifact: resume checks it
    "run-state__done-missing-artifact.json": (rs_, lambda x: x["stages"][1].update(artifact=None)),
    "run-state__skipped-missing-reason.json": (rs_, lambda x: x["stages"][0].pop("reason")),
    # stages 6 and 7 are not run by a2dlc run
    "run-state__stage-8.json": (rs_, lambda x: x["stages"].append({"id": "8", "status": "pending", "artifact": None, "iterations": 0, "started_at": None, "finished_at": None})),
    # the value of a variable in the ledger would be a credential written to disk
    "run-ledger__env-with-value.json": (lg, lambda x: x["entries"][0].update(env_keys=["GEMINI_API_KEY=abc"])),
    "run-ledger__missing-argv-hash.json": (lg, lambda x: x["entries"][0].pop("argv_sha256")),
    "run-ledger__hash-short.json": (lg, lambda x: x["entries"][1].update(hash="sha256:e7f6c011")),
    # block 2: Saboteur. Signatures carry no bodies: a signature with a body would be the builder's code in /in
    "engine-signatures-response__symbol-with-body.json": (sg, lambda x: x["files"][0]["symbols"][0].update(body="return sum(xs) / len(xs)")),
    "engine-signatures-response__missing-lines.json": (sg, lambda x: x["files"][0]["symbols"][0].pop("lines")),
    # the saboteur attacks concrete lines: a request without uncovered has nothing to cover
    "role-request__saboteur-missing-uncovered.json": (rq, lambda x: x["payload"].update(uncovered=[])),
    # without tests_paths the core could not reject a "test" that overwrites a source through the overlay
    "role-request__saboteur-missing-tests-paths.json": (rq, lambda x: x["payload"].pop("tests_paths")),
    # a test without a hash cannot be checked against what was left in /out
    "role-response__saboteur-test-missing-hash.json": (rr, lambda x: x["payload"]["test_files"][0].pop("sha256")),
    "role-response__saboteur-payload-foreign.json": (rr, lambda x: x.update(payload={"diff": "..."})),
    # block 4: intent, Router and CB-03. A criticality outside the order low < medium < high < critical cannot be routed
    "intent__criticality-unknown.json": (read("intent__greenfield-high.json"), lambda x: x.update(criticality="urgent")),
    # a negative cap would trigger CB-03 before launching anything; 0 neither: without a cap, null
    "intent__budget-negative.json": (read("intent__greenfield-high.json"), lambda x: x["budget"].update(max_cost_usd=-1)),
    "intent__budget-cost-zero.json": (read("intent__greenfield-high.json"), lambda x: x["budget"].update(max_cost_usd=0)),
    # 0 rounds would fall back to the default silently: the minimum is 1
    "intent__rounds-zero.json": (read("intent__greenfield-high.json"), lambda x: x["budget"].update(max_tier1_adversarial_rounds=0)),
    "intent__missing-goal.json": (read("intent__greenfield-high.json"), lambda x: x.pop("goal")),
    "intent__goal-empty.json": (read("intent__greenfield-high.json"), lambda x: x.update(goal="")),
    "intent__missing-budget.json": (read("intent__lite-missing-limit.json"), lambda x: x.pop("budget")),
    "intent__mode-unknown.json": (read("intent__lite-missing-limit.json"), lambda x: x.update(mode="bugfix")),
    "intent__extra-field.json": (read("intent__lite-missing-limit.json"), lambda x: x.update(owner="aldemar")),
    # Stage 7: compensation is one of the three strategies of §5 (absent or null means "there is none"); a ring starts at 1
    "intent__delivery-strategy-unknown.json": (read("intent__delivery-writes.json"), lambda x: x["delivery"]["compensation"].update(strategy="undo")),
    "intent__delivery-ring-zero.json": (read("intent__delivery-writes.json"), lambda x: x["delivery"].update(rings=[0, 100])),
    # writes is mandatory (without it CB-06 cannot be judged); empty rings would fall back to the default silently
    "intent__delivery-missing-writes.json": (read("intent__delivery-writes.json"), lambda x: x["delivery"].pop("writes")),
    "intent__delivery-rings-empty.json": (read("intent__delivery-writes.json"), lambda x: x["delivery"].update(rings=[])),
    # Stage 6: a consumer without a command runs nothing; migrations without paths would not know what to check
    "config__integration-consumer-missing-command.json": (read("config__integration.json"), lambda x: x["integration"]["consumers"][1].pop("command")),
    "config__integration-migrations-missing-paths.json": (read("config__integration.json"), lambda x: x["integration"]["migrations"].pop("paths")),
    "config__integration-consumer-name-uppercase.json": (read("config__integration.json"), lambda x: x["integration"]["consumers"][0].update(name="Billing-Web")),
    "integration__failed-missing-tests-reds.json": (read("integration__consumer-broken.json"), lambda x: x["consumers"][0].update(failed_tests=[])),
    "integration__status-unknown.json": (read("integration__pass.json"), lambda x: x["consumers"][0].update(status="green")),
    "delivery__blocked-by-unknown.json": (read("delivery__canary.json"), lambda x: x.update(blocked_by="CB-01")),
    "delivery__flag-missing-prefix.json": (read("delivery__canary.json"), lambda x: x.update(feature_flag="settlement")),
    "delivery__missing-max-ring.json": (read("delivery__cb06-refactor.json"), lambda x: x.pop("max_ring")),
    "route__track-unknown.json": (read("route__n2-critical-path.json"), lambda x: x.update(track="N=3")),
    # a decision without a reason cannot be audited
    "route__missing-reasons.json": (read("route__n2-critical-path.json"), lambda x: x.update(reasons=[])),
    "route__criticality-unknown.json": (read("route__n1-lite.json"), lambda x: x.update(criticality_effective="max")),
    # the per-role cost breakdown of the protocol: the oracle spends no tokens
    "run-state__by-role-unknown.json": (read("run-state__by-role.json"), lambda x: x["budget"]["by_role"].update(oracle=0.1)),
    "run-state__by-role-negative.json": (read("run-state__by-role.json"), lambda x: x["budget"]["by_role"].update(saboteur=-0.42)),
    # block 3: Builder N=1 + Tier 0. ok with findings, or red without any, would make the loop unable to know whether to stop
    "engine-tier0-response__ok-with-findings.json": (read("engine-tier0-response__py-failures.json"), lambda x: x.update(ok=True)),
    "engine-tier0-response__fails-missing-findings.json": (read("engine-tier0-response__py-ok.json"), lambda x: x.update(ok=False)),
    "engine-tier0-response__kind-unknown.json": (read("engine-tier0-response__py-failures.json"), lambda x: x["findings"][0].update(kind="test")),
    # with tier0 the budget is built by the core and carries only probes: CrossHair's does not apply
    "engine-request__tier0-budget-foreign.json": (read("engine-request__py-tier0.json"), lambda x: x["budget"].update(per_condition_timeout_s=10)),
    # a probe may lack expr (Tier 0 does not run it), but an empty expr does not make an assertion
    "probes__expr-empty.json": (read("probes__py.json"), lambda x: x["probes"][0].update(expr="")),
    "engine-request__tier0-probe-missing-expr.json": (read("engine-request__py-tier0.json"), lambda x: x["budget"]["probes"][0].pop("expr")),
    "candidate__tree-hash-short.json": (read("candidate__builder.json"), lambda x: x.update(tree_hash="9d45962")),
    "candidate__missing-unchecked.json": (read("candidate__builder.json"), lambda x: x.pop("unchecked")),
    # the feedback from the previous round is part of the request even if null: without it the builder does not know which round it is in
    "role-request__builder-missing-feedback.json": (read("role-request__builder.json"), lambda x: x["payload"].pop("tier0_feedback")),
    "role-request__builder-missing-workdir.json": (read("role-request__builder.json"), lambda x: x.update(workdir=None)),
    "role-response__builder-payload-foreign.json": (read("role-response__builder-ok.json"), lambda x: x.update(payload={"test_files": [], "targets": []})),
    # block 5: Stage 5, mutation bounded to the diff. Without the changed lines the engine would mutate the whole file
    "engine-request__mutate-missing-lines.json": (read("engine-request__py-mutate.json"), lambda x: x["budget"].pop("changed_lines")),
    "engine-request__mutate-missing-limit.json": (read("engine-request__py-mutate.json"), lambda x: x["budget"].pop("max_mutants")),
    # the id is the name of the folder in /io/mutants: with / it would leave that folder
    "engine-mutate-response__id-with-slash.json": (read("engine-mutate-response__py.json"), lambda x: x["mutants"][0].update(id="../src")),
    "engine-mutate-response__operator-unknown.json": (read("engine-mutate-response__py.json"), lambda x: x["mutants"][0].update(operator="swap_args")),
    "engine-mutate-response__missing-not-generated.json": (read("engine-mutate-response__py.json"), lambda x: x.pop("not_generated")),
    # a killed mutant without the test that killed it cannot be audited; a live one has no killer
    "mutation__killed-missing-killed-by.json": (read("mutation__kill-gate.json"), lambda x: x["mutants"][0].update(killed_by=[])),
    "mutation__survived-with-killed-by.json": (read("mutation__kill-gate.json"), lambda x: x["mutants"][0].update(status="survived")),
    "mutation__not-run-missing-reason.json": (read("mutation__limit.json"), lambda x: x["mutants"][1].pop("reason")),
    # what did not run does not count as killed: with a not_run the gate cannot give PASS
    "mutation__pass-with-not-run.json": (read("mutation__limit.json"), lambda x: (x.update(verdict="PASS"), x.update(not_generated=[]))),
    "mutation__state-unknown.json": (read("mutation__kill-gate.json"), lambda x: x["mutants"][3].update(status="timeout")),
    "mutation__pass-with-inconclusive.json": (read("mutation__kill-gate.json"), lambda x: (x.pop("reason"), x.update(verdict="PASS"))),
    "config__mutation-kill-rate-over-one.json": (read("config__mutation.json"), lambda x: x["mutation"].update(min_kill_rate=80)),
    "escalation__mutant-missing-evidence.json": (read("escalation__mutant-alive.json"), lambda x: x["items"][0]["evidence"].pop("mutant")),
    "escalation__mutant-missing-needs.json": (read("escalation__mutant-alive.json"), lambda x: x["items"][0].pop("needs")),
    # the saboteur does not receive code: a survivor with the mutated text is not in the contract
    "role-request__saboteur-survivor-with-code.json": (read("role-request__saboteur-survivors.json"), lambda x: x["payload"]["survivors"][0].update(code="return total - 1")),
    "role-request__saboteur-survivors-empty.json": (read("role-request__saboteur-survivors.json"), lambda x: x["payload"].update(survivors=[])),
    # block 6: Stage 1. A strict π_S with relaxations would be over-masking without a human probe; relaxed without saying which, too
    "slice-contract__strict-with-relaxations.json": (read("slice-contract__alpha.json"), lambda x: x["pi_s"].update(relaxations=["ignore trace_id"])),
    "slice-contract__relaxed-missing-relaxations.json": (read("slice-contract__alpha.json"), lambda x: x["pi_s"].update(equivalence="relaxed")),
    "slice-contract__missing-pi-s.json": (read("slice-contract__alpha.json"), lambda x: x.pop("pi_s")),
    "slice-contract__source-unknown.json": (read("slice-contract__alpha.json"), lambda x: x.update(source="inquisitor")),
    "slice-contract__probe-kind-unknown.json": (read("slice-contract__alpha.json"), lambda x: x["probes"][0].update(kind="smoke")),
    "slice-contract__probe-assumption-empty.json": (read("slice-contract__consensus.json"), lambda x: x["probes"][1].update(assumption="")),
    # a function without a signature (not even null) does not say whether the specifier fixed it
    "slice-contract__function-missing-signature.json": (read("slice-contract__alpha.json"), lambda x: x["functions"][0].pop("signature")),
    "assumption-catalogue__empty.json": (read("assumption-catalogue__numeric.json"), lambda x: x.update(entries=[])),
    "capture__missing-selection.json": (read("capture__refactor.json"), lambda x: x.pop("selection")),
    # the signatures in the capture do not carry bodies either
    "capture__signature-with-body.json": (read("capture__refactor.json"), lambda x: x["signatures"][0]["symbols"][0].update(body="return sum(xs) / len(xs)")),
    # a probes.json without the hash of S* cannot be tied to the contract that fixed it
    "probes__missing-contract-hash.json": (read("probes__consensus.json"), lambda x: x.pop("contract_hash")),
    "role-request__specifier-missing-intent.json": (read("role-request__specifier-beta.json"), lambda x: x["payload"].pop("intent")),
    "role-request__specifier-with-workdir.json": (read("role-request__specifier-beta.json"), lambda x: x.update(workdir="/work")),
    "role-request__inquisitor-missing-beta.json": (read("role-request__inquisitor.json"), lambda x: x["payload"].pop("contract_beta")),
    # a retry says why the previous answer was rejected; an empty reason tells the role nothing
    "role-request__specifier-empty-rejection.json": (read("role-request__specifier-beta-retry.json"), lambda x: x["payload"].update(rejection="")),
    "role-request__inquisitor-empty-rejection.json": (read("role-request__inquisitor-retry.json"), lambda x: x["payload"].update(rejection="")),
    "role-response__specifier-missing-contract.json": (read("role-response__specifier-alpha-ok.json"), lambda x: x.update(payload={"contract_text": {}})),
    # S* comes from the Inquisitor only as a consensus: a candidate from a specifier does not make S*
    "role-response__inquisitor-contract-of-alpha.json": (read("role-response__inquisitor-ok.json"), lambda x: x["payload"]["contract"].update(source="specifier_alpha")),
    "role-response__inquisitor-discrepancy-missing-resolution.json": (read("role-response__inquisitor-ok.json"), lambda x: x["payload"]["discrepancies"][0].pop("resolution")),
    "escalation__spec-divergence-missing-target.json": (read("escalation__spec-divergence.json"), lambda x: x["items"][0].pop("target")),
    # block A (roles/): env are non-secret variables; the core sets its own and the names follow the ledger's pattern
    "roles__env-overwrites-variable-of-the-core.json": (ro, lambda x: x["roles"]["saboteur"]["env"].update(A2DLC_ROLE="builder_primary")),
    "roles__env-overwrites-timeout-of-the-core.json": (ro, lambda x: x["roles"]["saboteur"]["env"].update(A2DLC_TIMEOUT_S="1")),
    # image goes as a positional argument of docker run: starting with "-" would make it a flag (--volume=/:/host)
    "roles__image-is-a-flag-of-docker.json": (ro, lambda x: x["roles"]["saboteur"].update(image="--volume=/:/host")),
    "roles__image-with-space.json": (ro, lambda x: x["roles"]["saboteur"].update(image="a2dlc-roles:0.1 --privileged")),
    "roles__env-key-lowercase.json": (ro, lambda x: x["roles"]["saboteur"]["env"].update(a2dlc_llm="gemini")),
    "roles__env-value-no-chain.json": (ro, lambda x: x["roles"]["saboteur"]["env"].update(A2DLC_LLM=1)),
    # block 7
    # smt is SMT-LIB text: an empty list says nothing and does not mean "no smt" (that is the absent key)
    "slice-contract__smt-empty.json": (read("slice-contract__smt.json"), lambda x: x["functions"][0].update(smt=[])),
    "slice-contract__criticality-unknown.json": (read("slice-contract__smt.json"), lambda x: x["assumptions"][1].update(criticality="urgent")),
    "contract-lint__missing-trace-coverage.json": (read("contract-lint__warnings.json"), lambda x: x.pop("trace_coverage")),
    "contract-lint__severity-unknown.json": (read("contract-lint__warnings.json"), lambda x: x["findings"][0].update(severity="info")),
    "contract-lint__check-unknown.json": (read("contract-lint__warnings.json"), lambda x: x["findings"][0].update(check="style")),
    # without the hash of the translated contract it is not known which S* the translation is about
    "backtranslation__missing-contract-hash.json": (read("backtranslation__media.json"), lambda x: x.pop("contract_hash")),
    "backtranslation__prose-empty.json": (read("backtranslation__media.json"), lambda x: x.update(prose="")),
    # matches and discrepancies have to say the same thing
    "backtranslation-review__matches-with-discrepancies.json": (read("backtranslation-review__mismatch.json"), lambda x: x.update(matches=True)),
    "backtranslation-review__mismatch-missing-discrepancies.json": (read("backtranslation-review__match.json"), lambda x: x.update(matches=False)),
    "z3-request__function-missing-smt.json": (read("z3-request__transfer.json"), lambda x: x["functions"][0].update(smt=[])),
    "z3-response__sat-missing-model.json": (read("z3-response__transfer.json"), lambda x: x["results"][0].update(model=None)),
    "z3-response__unsat-missing-core.json": (read("z3-response__transfer.json"), lambda x: x["results"][1].update(reason=None)),
    "z3-response__state-unknown.json": (read("z3-response__transfer.json"), lambda x: x["results"][0].update(status="valid")),
    # the Back-translator sees only S*: an intent in its payload would break its blindness
    "role-request__back-translator-with-intent.json": (read("role-request__back-translator.json"), lambda x: x["payload"].update(intent={"goal": "x"})),
    # the Inquisitor of 1.5 does not receive the request of Stage 1, nor does the one of 1 receive that of 1.5
    "role-request__inquisitor-bt-with-payload-of-stage1.json": (read("role-request__inquisitor.json"), lambda x: x.update(stage="1.5")),
    "role-request__inquisitor-stage1-with-payload-bt.json": (read("role-request__inquisitor-bt.json"), lambda x: x.update(stage="1")),
    "role-request__inquisitor-in-stage4.json": (read("role-request__inquisitor-bt.json"), lambda x: x.update(stage="4")),
    "role-response__back-translator-missing-prose.json": (read("role-response__back-translator-ok.json"), lambda x: x["payload"].pop("prose")),
    "role-response__inquisitor-bt-matches-with-discrepancies.json": (read("role-response__inquisitor-bt-ok.json"), lambda x: x["payload"].update(matches=True)),
    "escalation__mismatch-kind-unknown.json": (read("escalation__stage15.json"), lambda x: x["items"][0].update(kind="bt_mismatch")),
    "engine-request__relaxations-duplicate.json": (read("engine-request__py-relaxations.json"), lambda x: x.update(equivalence_relaxations=["ignore_order", "ignore_order"])),
    "engine-response__unsupported-not-list.json": (read("engine-response__py-relaxations.json"), lambda x: x.update(equivalence_unsupported="ignore_order")),
    # identical names a symbol as file::name, like the signatures operation; a bare name cannot be matched to a file
    "engine-response__identical-without-file.json": (read("engine-response__py-identical.json"), lambda x: x.update(identical=["suma"])),
    # block 8: N=2 + Occam selection. A zero instead of null would make the candidate the engine could not measure win
    "engine-complexity-response__missing-ast-nodes.json": (read("engine-complexity-response__py.json"), lambda x: x["files"][0].pop("ast_nodes")),
    "engine-complexity-response__loc-negative.json": (read("engine-complexity-response__py.json"), lambda x: x["files"][0].update(loc_added=-1)),
    "engine-request__complexity-op-unknown.json": (read("engine-request__py-complexity.json"), lambda x: x.update(operation="occam")),
    # the selection compares two different builders: two primaries are not N=2
    "selection__two-primary.json": (read("selection__wins-orthogonal.json"), lambda x: x["candidates"][1].update(builder="builder_primary")),
    "selection__a-candidate.json": (read("selection__wins-orthogonal.json"), lambda x: x["candidates"].pop()),
    "selection__missing-reason.json": (read("selection__wins-orthogonal.json"), lambda x: x.pop("reason")),
    "selection__chooses-other-role.json": (read("selection__wins-orthogonal.json"), lambda x: x.update(selected="saboteur")),
    "candidate__n2-three-candidates.json": (read("candidate__n2.json"), lambda x: x["candidates"].append(dict(x["candidates"][0]))),
    "candidate__dropped-missing-reason.json": (read("candidate__n2-one-drops.json"), lambda x: x["dropped"][0].pop("reason")),
    "verdict__contract-hash-missing-prefix.json": (read("verdict__run-n2.json"), lambda x: x.update(contract_hash="4b825dc642cb6eb9a060e54bf8d69288fbee4904a0b1c2d3e4f5061728394a5b")),
    "verdict__stage-8.json": (read("verdict__run-n2.json"), lambda x: x.update(stage="8")),
    "escalation__cluster-long.json": (read("escalation__n2-cluster.json"), lambda x: x["items"][0].update(cluster_id="clu-5e6f7a8b9c0d1e")),
    "escalation__builder-saboteur.json": (read("escalation__n2-cluster.json"), lambda x: x["items"][0].update(builder="saboteur")),
    # the metrics of S* are counts: a negative one does not measure anything
    "route__rule-count-negative.json": (read("route__n2-critical-path.json"), lambda x: x.update(rule_count=-1)),
    "route__state-mutation-depth-negative.json": (read("route__n2-critical-path.json"), lambda x: x.update(state_mutation_depth=-2)),
    "route__metrics-reason-empty.json": (read("route__n1-lite.json"), lambda x: x.update(metrics_reason="")),
    # @a2dlc header (§6.3): the mode is an enum of the contract and contract_hash is required even when null
    "role-request__builder-header-mode-unknown.json": (read("role-request__builder.json"), lambda x: x["payload"]["header"].update(mode="vibes")),
    "role-request__builder-header-missing-contract-hash.json": (read("role-request__builder.json"), lambda x: x["payload"]["header"].pop("contract_hash")),
    "intent__rejected-alternatives-empty.json": (read("intent__greenfield-high.json"), lambda x: x.update(rejected_alternatives=[""])),
    "candidate__headers-missing-duplicate.json": (read("candidate__builder.json"), lambda x: x.update(headers_missing=["web/app.ts", "web/app.ts"])),
    # CB-04 (§7, protected boundary of the intent): an empty list or an empty symbol protects nothing; a boundary without fields
    # does not either
    "intent__boundary-list-empty.json": (read("intent__boundary.json"), lambda x: x["boundary"].update(forbidden_paths=[])),
    "intent__boundary-symbol-empty.json": (read("intent__boundary.json"), lambda x: x["boundary"].update(do_not_refactor=[""])),
    "intent__boundary-empty.json": (read("intent__boundary.json"), lambda x: x.update(boundary={})),
    # forbidden_side_effects is a declared key of boundary (intent.schema.json): this one is invalid by its enum value "red"
    "intent__boundary-extra-field.json": (read("intent__boundary.json"), lambda x: x["boundary"].update(forbidden_side_effects=["red"])),
    "role-request__builder-boundary-empty.json": (read("role-request__builder-boundary.json"), lambda x: x["payload"]["boundary"].update(do_not_refactor=[])),
    # a boundary escalation without a message does not say which line crossed it; without "::" it does not match a target_id
    "escalation__cb04-missing-message.json": (read("escalation__cb04.json"), lambda x: x["items"][0]["evidence"].pop("message")),
    "escalation__cb04-target-missing-separator.json": (read("escalation__cb04.json"), lambda x: x["items"][0].update(target="docs/notas.md")),
    # CB-02b: tier_up has the shape of a role and does not allow its own tier_up; builder_exhaustion is not resolved with a waiver
    "roles__tier-up-nested.json": (read("roles__tier-up.json"), lambda x: x["roles"]["builder_primary"]["tier_up"].update(tier_up=dict(x["roles"]["builder_primary"]["tier_up"]))),
    "roles__tier-up-missing-argv.json": (read("roles__tier-up.json"), lambda x: x["roles"]["builder_primary"]["tier_up"].pop("argv")),
    "roles__tier-up-mounts.json": (read("roles__tier-up.json"), lambda x: x["roles"]["builder_primary"]["tier_up"].update(mounts=["/repo:/work"])),
    "escalation__cb02b-with-waiver.json": (read("escalation__cb02b.json"), lambda x: x["items"][0].update(resolve_with=["enforce_new_behavior"])),
    # CB-05: a baseline that could not be established is not resolved with a waiver and always says why (evidence.message)
    "escalation__cb05-with-waiver.json": (read("escalation__cb05.json"), lambda x: x["items"][0].update(resolve_with=["enforce_new_behavior"])),
    "escalation__cb05-missing-message.json": (read("escalation__cb05.json"), lambda x: x["items"][0]["evidence"].pop("message")),
    # baseline (Stage -1): zero tests is not a baseline; red tests are not repeated; a new field does not slip in
    "baseline__zero-tests.json": (read("baseline__run-real.json"), lambda x: x.update(tests=0)),
    "baseline__red-duplicate.json": (read("baseline__run-real.json"), lambda x: x.update(preexisting_red=["t::a", "t::a"])),
    "baseline__extra-field.json": (read("baseline__run-real.json"), lambda x: x.update(green=True)),
    "baseline__missing-cached.json": (read("baseline__green-from-cache.json"), lambda x: x.pop("cached")),
    # equivalent-mutant pruning (Stage 5 step 2): "pruned" is required and its reason comes from the engine's closed list
    "engine-mutate-response__missing-pruned.json": (read("engine-mutate-response__py.json"), lambda x: x.pop("pruned")),
    "engine-mutate-response__pruned-reason-unknown.json": (read("engine-mutate-response__py.json"), lambda x: x["pruned"][0].update(reason="equivalent: whatever")),
    "engine-mutate-response__pruned-line-zero.json": (read("engine-mutate-response__py.json"), lambda x: x["pruned"][0].update(line=0)),
    "mutation__missing-pruned-equivalent.json": (read("mutation__kill-gate.json"), lambda x: x.pop("pruned_equivalent")),
    "mutation__pruned-missing-reason.json": (read("mutation__kill-gate.json"), lambda x: x["pruned_equivalent"][0].pop("reason")),
    # Tier 0 policy: a complexity maximum of 0 is not a limit; a new key is not silently ignored
    "config__tier0-max-complexity-zero.json": (read("config__tier0-policy.json"), lambda x: x["tier0"].update(max_complexity=0)),
    "config__tier0-unknown-key.json": (read("config__tier0-policy.json"), lambda x: x["tier0"].update(ignore_policy=True)),
    # the request carries the repo's policy as is: an invented name does not apply it
    "engine-request__tier0-policy-unknown-key.json": (read("engine-request__py-tier0-policy.json"), lambda x: x["budget"]["policy"].update(forbidden_modules=["os"])),
    "engine-tier0-response__reviewed-no-bool.json": (read("engine-tier0-response__py-policy.json"), lambda x: x.update(reviewed="yes")),
    "candidate__tier0-reviewed-no-bool.json": (read("candidate__builder-unreviewed.json"), lambda x: x.update(tier0_reviewed=0)),
    # wave 0 (v3.4): each new field has at least one way to be wrong
    "verdict__diff-budget-cut-missing-reason.json": (read("verdict__diff-budget.json"), lambda x: x["diff_budget"]["cut"][0].pop("reason")),
    "verdict__equivalence-gate-unknown-status.json": (read("verdict__diff-budget.json"), lambda x: x["test_gates"]["equivalence_s"].update(status="skipped")),
    "config__trace-coverage-over-one.json": (read("config__v3-gates.json"), lambda x: x["tests"]["trace_coverage"].update(min_branch=1.5)),
    "config__golden-master-missing-cases.json": (read("config__v3-gates.json"), lambda x: x["tests"]["golden_master"].pop("cases")),
    "config__protected-paths-duplicate.json": (read("config__v3-gates.json"), lambda x: x.update(protected_paths=["a/**", "a/**"])),
    "escalation__lemma-failed-missing-counterexample.json": (read("escalation__v3-kinds.json"), lambda x: x["items"][2]["evidence"].pop("counterexample")),
    "escalation__protected-path-missing-message.json": (read("escalation__v3-kinds.json"), lambda x: x["items"][0]["evidence"].pop("message")),
    "escalation__answer-id-empty.json": (read("escalation__v3-kinds.json"), lambda x: x["items"][4].update(answer_id="")),
    "intent__sla-op-unknown.json": (read("intent__v3-fields.json"), lambda x: x["slas"][0].update(op="==")),
    "intent__side-effect-unknown.json": (read("intent__v3-fields.json"), lambda x: x["boundary"]["forbidden_side_effects"].append("db.drop")),
    "intent__criticality-tag-unknown.json": (read("intent__v3-fields.json"), lambda x: x["criticality_tags"].append("payments")),
    "intent__max-tokens-zero.json": (read("intent__v3-fields.json"), lambda x: x["budget"].update(max_tokens=0)),
    "intent__change-kind-unknown.json": (read("intent__v3-fields.json"), lambda x: x.update(change_kind="hotfix")),
    "slice-contract__relaxation-free-text.json": (read("slice-contract__io-lemmas.json"), lambda x: x["pi_s"]["relaxations"].append("compare receipts without trace_id")),
    "slice-contract__io-missing-taxonomy.json": (read("slice-contract__io-lemmas.json"), lambda x: x["io_contract"].pop("error_taxonomy")),
    "slice-contract__lemma-missing-postcondition.json": (read("slice-contract__io-lemmas.json"), lambda x: x["functions"][0]["lemmas"][0].pop("postcondition")),
    "slice-contract__invariant-verification-unknown.json": (read("slice-contract__io-lemmas.json"), lambda x: x["invariants"][0].update(verification="z3")),
    "roles__egress-allow-with-space.json": (read("roles__egress.json"), lambda x: x["roles"]["saboteur"].update(egress_allow=["api openai.com"])),
    "roles__family-exception-empty-reason.json": (read("roles__egress.json"), lambda x: x["family_exception"].update(reason="")),
    "engine-request__fuzz-missing-seed.json": (read("engine-request__py-fuzz.json"), lambda x: x["budget"].pop("seed")),
    "engine-request__golden-missing-cases.json": (read("engine-request__py-golden-record.json"), lambda x: x["budget"].pop("cases")),
    "engine-request__deps-graph-with-budget.json": (read("engine-request__py-deps-graph.json"), lambda x: x["budget"].update(depth=3)),
    "engine-request__lemmas-mode-unknown.json": (read("engine-request__py-lemmas.json"), lambda x: x["budget"].update(mode="prove")),
    "engine-fuzz-response__finding-without-seed.json": (read("engine-fuzz-response__py.json"), lambda x: x["findings"][0].pop("seed")),
    "engine-lemmas-response__fail-without-counterexample.json": (read("engine-lemmas-response__py.json"), lambda x: x["lemmas"][1].pop("counterexample")),
    "engine-lemmas-response__status-unknown.json": (read("engine-lemmas-response__py.json"), lambda x: x["lemmas"][0].update(status="proved")),
    "engine-deps-response__edge-missing-to.json": (read("engine-deps-response__py.json"), lambda x: x["edges"][0].pop("to")),
    "engine-golden-response__hash-short.json": (read("engine-golden-response__py.json"), lambda x: x["cases"][0].update(output_hash="sha256:abc")),
    "answers__probe-missing-reason.json": (read("answers__cli.json"), lambda x: x["answers"][0].pop("reason")),
    "answers__contract-hash-missing-prefix.json": (read("answers__cli.json"), lambda x: x.update(contract_hash="b" * 64)),
    "golden__cases-empty.json": (read("golden__refactor.json"), lambda x: x.update(cases=[])),
    "golden__extra-field.json": (read("golden__refactor.json"), lambda x: x.update(green=True)),
    "deps-allow__ecosystem-unknown.json": (read("deps-allow__mixed.json"), lambda x: x["allow"][0].update(ecosystem="maven")),
    "deps-allow__name-empty.json": (read("deps-allow__mixed.json"), lambda x: x["allow"][1].update(name="")),
    "integration__discovered-via-unknown.json": (read("integration__discovered.json"), lambda x: x["discovered"][0].update(via="http")),
    "mutation__pass-with-unsupported-files.json": (read("mutation__pass.json"), lambda x: x.update(unsupported_files=["src/lang.ts"])),
    "mutation__unsupported-files-empty-name.json": (read("mutation__unsupported-files.json"), lambda x: x.update(unsupported_files=[""])),
    "candidate__dependency-allowed-string.json": (read("candidate__lemmas-deps.json"), lambda x: x["dependency_changes"][0].update(allowed="yes")),
    "candidate__lemma-status-unknown.json": (read("candidate__lemmas-deps.json"), lambda x: x["lemmas"][0].update(status="ok")),
    "delivery__writes-null-threshold.json": (read("delivery__run-real.json"), lambda x: x["sentinel"].update(error_rate_delta_max=None)),
    "delivery__canary-empty-paths.json": (read("delivery__run-real.json"), lambda x: x.update(canary={"paths": []})),
    "delivery__parity-paths-empty.json": (read("delivery__cb06-refactor.json"), lambda x: x["parity"].update(paths=[])),
    "run-ledger__network-unknown.json": (read("run-ledger__bridge.json"), lambda x: x["entries"][1].update(network="host")),
    "run-state__spent-tokens-negative.json": (read("run-state__spent-tokens.json"), lambda x: x["budget"].update(spent_tokens=-1)),
    "capture__branch-coverage-over-one.json": (read("capture__traces.json"), lambda x: x.update(branch_coverage=1.5)),
    "capture__trace-missing-branch-ids.json": (read("capture__traces.json"), lambda x: x["traces"][0].pop("branch_ids")),
    "baseline__digest-not-sha256.json": (read("baseline__run-real.json"), lambda x: x.update(image_digest="md5:abc")),
    "baseline__reruns-zero.json": (read("baseline__run-real.json"), lambda x: x.update(reruns=0)),
    "baseline__stable-missing.json": (read("baseline__run-real.json"), lambda x: x.pop("stable")),
    # v3.4 (wave 2a): the new fields and the new schema each have at least one way to be wrong
    "engine-traces-response__runner-unknown.json": (read("engine-traces-response__sign.json"), lambda x: x.update(runner="rspec")),
    "engine-traces-response__coverage-over-one.json": (read("engine-traces-response__sign.json"), lambda x: x.update(branch_coverage=1.5)),
    "engine-traces-response__missing-error.json": (read("engine-traces-response__sign.json"), lambda x: x.pop("error")),
    "engine-traces-response__extra-field.json": (read("engine-traces-response__sign.json"), lambda x: x.update(harness="pytest-cov")),
    "engine-request__capture-traces-budget-foreign.json": (read("engine-request__py-capture-traces.json"), lambda x: x["budget"].update(retries=2)),
    "engine-request__budget-null.json": (read("engine-request__py-tier0.json"), lambda x: x.update(budget=None)),
    "engine-request__capture-traces-timeout-zero.json": (read("engine-request__py-capture-traces.json"), lambda x: x["budget"].update(timeout_s=0)),
    "role-request__propose-missing-diff.json": (read("role-request__propose-properties.json"), lambda x: x["payload"].pop("diff")),
    "role-request__propose-language-unknown.json": (read("role-request__propose-properties.json"), lambda x: x["payload"].update(engine_language="rust")),
    # a target is <file>::<symbol>: a bare symbol does not say where the property goes
    "role-response__propose-target-missing-separator.json": (read("role-response__propose-properties-ok.json"), lambda x: x["payload"]["properties"][0].update(target="media")),
    "role-response__builder-lemma-missing-postcondition.json": (read("role-response__builder-lemmas-ok.json"), lambda x: x["payload"]["lemmas"][0].pop("postcondition")),
    "engine-stub-response__modified-tests-duplicate.json": (read("engine-stub-response__py-modified-tests.json"), lambda x: x.update(modified_tests=["tests/t.py::a", "tests/t.py::a"])),
    "engine-fuzz-response__unsupported-missing-reason.json": (read("engine-fuzz-response__unsupported.json"), lambda x: x["unsupported"][0].pop("reason")),
    "engine-deps-response__error-not-string.json": (read("engine-deps-response__error.json"), lambda x: x.update(error=5)),
    "engine-response__diff-budget-cut-missing-target.json": (read("engine-response__diff-budget.json"), lambda x: x["diff_budget"]["cut"][0].pop("target")),
    "route__lemmas-required-missing.json": (read("route__n2-critical-path.json"), lambda x: x.pop("lemmas_required")),
    "config__integration-consumer-auto-string.json": (read("config__integration.json"), lambda x: x["integration"]["consumers"][1].update(auto="yes")),
    "intent__delivery-sentinel-without-thresholds.json": (read("intent__delivery-writes.json"), lambda x: x["delivery"].update(writes=True, sentinel={"invariants": ["x"]})),
    # a branch id is <from line>-><to line>: b1 names no decision
    "capture__branch-id-format.json": (read("capture__traces.json"), lambda x: x["traces"][0].update(branch_ids=["b1"])),
    # pluggable checks (protocol §5 Checks, ADR meta/0006): severity has no default, no host command, a check never reports its own error
    "config__checks-missing-severity.json": (read("config__checks.json"), lambda x: x["checks"][0].pop("severity")),
    "config__checks-severity-unknown.json": (read("config__checks.json"), lambda x: x["checks"][0].update(severity="warning")),
    "config__checks-missing-image.json": (read("config__checks.json"), lambda x: x["checks"][0].pop("image")),
    "config__checks-host-command.json": (read("config__checks.json"), lambda x: x["checks"][0].update(host_command="ruff check .")),
    "config__checks-fire-on-unknown-stage.json": (read("config__checks.json"), lambda x: x["checks"][1].update(fire_on=["stage:9"])),
    "config__checks-fire-on-verify.json": (read("config__checks.json"), lambda x: x["checks"][1].update(fire_on=["verify"])),
    "config__checks-timeout-zero.json": (read("config__checks.json"), lambda x: x["checks"][0].update(timeout_s=0)),
    "config__checks-matches-empty.json": (read("config__checks.json"), lambda x: x["checks"][0].update(matches=[])),
    "check-response__status-error.json": (read("check-response__ran-findings.json"), lambda x: x.update(status="error", findings=[])),
    "check-response__pass-with-findings.json": (read("check-response__ran-findings.json"), lambda x: x.update({"pass": True})),
    "check-response__not-applicable-with-findings.json": (read("check-response__ran-findings.json"), lambda x: x.update(status="not_applicable")),
    "check-response__finding-without-file.json": (read("check-response__ran-findings.json"), lambda x: x["findings"][0].update(target="F401 unused import")),
    "check-response__findings-count.json": (read("check-response__ran-findings.json"), lambda x: x.update(findings_count=2)),
    "verdict__check-error-without-reason.json": (read("verdict__checks.json"), lambda x: x["checks"][2].pop("reason")),
    "verdict__check-error-pass.json": (read("verdict__checks.json"), lambda x: x["checks"][2].update({"pass": True})),
    "verdict__check-ran-without-findings-field.json": (read("verdict__checks.json"), lambda x: x["checks"][0].pop("findings")),
    "verdict__check-status-unknown.json": (read("verdict__checks.json"), lambda x: x["checks"][1].update(status="skipped")),
    "verdict__check-finding-without-waived.json": (read("verdict__checks.json"), lambda x: x["checks"][0]["findings"][0].pop("waived")),
    "waivers__accept-finding-free-target.json": (read("waivers__accept-finding.json"), lambda x: x["waivers"][0].update(target="ruff")),
    "escalation__check-failed-missing-message.json": (read("escalation__check-failed.json"), lambda x: x["items"][0]["evidence"].pop("message")),
    "escalation__check-failed-with-legacy-waiver.json": (read("escalation__check-failed.json"), lambda x: x["items"][0].update(resolve_with=["preserve_legacy"])),
    "escalation__check-failed-no-resolution.json": (read("escalation__check-failed.json"), lambda x: x["items"][0].update(resolve_with=[])),
}
for name, (base, breaks) in cases.items():
    doc = copy.deepcopy(base)
    breaks(doc)
    write(name, doc)
write("waivers__list-flat-of-the-pilot.json", copy.deepcopy(w["waivers"]))
print(len(cases) + 1, "invalid examples in", INVALID)
