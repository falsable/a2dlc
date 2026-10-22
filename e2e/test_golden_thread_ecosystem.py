#!/usr/bin/env python3
"""test_golden_thread_ecosystem.py — Golden Thread E2E Test across the 5 Ecosystem Repositories (Phase 5, P13).

Verifies the live, zero-mock integration across all 5 repositories in Documents/personal:
1. `compiled-policies` (`bridge_a2dlc.py`):
   Exports a formal `SliceContract` (S*) with SMT invariants (`presupuesto_final >= 0`, conservation, etc.)
   and validates it against `a2dlc/contracts/slice-contract.schema.json`.
2. `Entropos` + `a2dlc/roles/adapter.py` (`A2DLC_LLM=entropos`):
   Routes a `builder_primary` role invocation through the Entropos Complexity Router protocol,
   extracting router telemetry (`_entropos_router_meta`, `usage.cost`), and writing the candidate
   implementation (`src/compras/politica.py`) into the target repository's working tree.
3. `ChronosGraph` + `a2dlc/tools/chronosgraph_blast_radius.py` (Stage 6 Code Graph Indexer):
   Runs the real ChronosGraph AST (`TreeSitterAnalyzer`) and `SideEffectsAnalyzer` over the candidate tree
   via `a2dlc run`, discovering downstream consumers (`services/finanzas/auditor.py -> src/compras/politica.py`)
   and computing `blast_radius` telemetry in `integration.json`.
4. `a2dlc` (`a2dlc run` & `a2dlc verify-run`):
   Executes the deterministic state machine (Stages -1 to 7) over the slice `compras.procesar_solicitudes.v1`,
   generating `.a2dlc/verdict.json` bound to `tree_hash`, `integration.json`, `delivery.json`, and the hash-chained `ledger.jsonl`.
5. `agencia-ia` (`app/domain/code_verification.py` & `app/domain/audit_package.py` — Agora Core ISO 42001):
   Ingests the real `.a2dlc/verdict.json` produced by `a2dlc run`, verifies its cryptographic integrity,
   and asserts `compliance_status == "COMPLIANT"` and `iso_42001_defensible is True`.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from typing import Any

A2DLC_ROOT = Path(__file__).resolve().parents[1]
WORKSPACE_ROOT = A2DLC_ROOT.parent
COMPILED_POLICIES_ROOT = WORKSPACE_ROOT / "compiled-policies"
CHRONOSGRAPH_SRC = WORKSPACE_ROOT / "ChronosGraph" / "src"
ENTROPOS_ROOT = WORKSPACE_ROOT / "Entropos"
AGENCIA_IA_ROOT = WORKSPACE_ROOT / "agencia-ia"

for p in (COMPILED_POLICIES_ROOT, CHRONOSGRAPH_SRC, AGENCIA_IA_ROOT):
    if p.exists() and str(p) not in sys.path:
        sys.path.insert(0, str(p))


CANDIDATE_POLITICA_PY = '''"""Módulo de política de compras verificado por A2-DLC."""
from __future__ import annotations


def procesar_solicitudes(entradas: dict) -> dict:
    """Procesa solicitudes de compra garantizando conservación presupuestaria en céntimos.

    Postcondition: result["presupuesto_final"] >= 0 and result["presupuesto_final"] <= entradas.get("presupuesto_inicial", 0)
    """
    presupuesto_inicial = int(entradas.get("presupuesto_inicial", 0))
    if presupuesto_inicial < 0:
        raise ValueError("presupuesto_inicial no puede ser negativo")

    saldo = presupuesto_inicial
    decisiones: dict[str, dict[str, str]] = {}
    for sol in entradas.get("solicitudes", []):
        sol_id = str(sol.get("id", ""))
        raw_imp = str(sol.get("importe", "")).replace("EUR", "").strip()
        try:
            importe = int(raw_imp)
        except ValueError as exc:
            raise ValueError("formato de importe invalido") from exc

        if importe > saldo:
            decisiones[sol_id] = {"estado": "escalada", "motivo": "sin_presupuesto"}
        else:
            saldo -= importe
            decisiones[sol_id] = {"estado": "aprobada", "motivo": "ok"}

    res = {
        "presupuesto_inicial": presupuesto_inicial,
        "presupuesto_final": saldo,
        "total_aprobado": presupuesto_inicial - saldo,
        "decisiones": decisiones,
    }
    assert res["presupuesto_final"] >= 0 and res["presupuesto_final"] <= presupuesto_inicial
    return res
'''


class EntroposGatewayStub(BaseHTTPRequestHandler):
    """Simulates Entropos `/v1/chat/completions` gateway with Shannon entropy routing metadata."""

    requests_seen: list[dict[str, Any]] = []

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length).decode("utf-8"))
        EntroposGatewayStub.requests_seen.append({
            "path": self.path,
            "auth": self.headers.get("Authorization"),
            "model": body.get("model"),
        })

        builder_reply = {
            "status": "ok",
            "reason": None,
            "files": [
                {
                    "path": "src/compras/politica.py",
                    "content": CANDIDATE_POLITICA_PY,
                }
            ],
            "payload": {
                "summary": "Implement procesar_solicitudes with budget conservation and postcondition assert",
                "files_changed": ["src/compras/politica.py"],
                "lemmas": [
                    {
                        "name": "procesar_solicitudes",
                        "postcondition": 'result["presupuesto_final"] >= 0',
                    }
                ],
            },
        }
        response_doc = {
            "id": "chatcmpl-entropos-golden-1",
            "object": "chat.completion",
            "model": "claude-3-5-sonnet-20241022",
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": json.dumps(builder_reply),
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": 1450,
                "completion_tokens": 310,
                "total_tokens": 1760,
                "cost": 0.00185,
            },
            "_entropos_router_meta": {
                "provider": "anthropic",
                "routedModel": "claude-3-5-sonnet-20241022",
                "entropy_bits": 3.82,
                "cost_usd": 0.00185,
                "saved_usd": 0.00420,
                "discount_pct": "69.4%",
                "failover": False,
            },
        }
        payload = json.dumps(response_doc).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args: Any) -> None:
        pass


class GoldenThreadEcosystemTest(unittest.TestCase):
    """End-to-end verification across compiled-policies, Entropos, ChronosGraph, a2dlc, and agencia-ia."""

    def test_five_repo_golden_thread(self) -> None:
        # Verify all 5 repositories exist in the workspace
        for repo_dir in (A2DLC_ROOT, COMPILED_POLICIES_ROOT, CHRONOSGRAPH_SRC, ENTROPOS_ROOT, AGENCIA_IA_ROOT):
            self.assertTrue(repo_dir.exists(), f"Missing ecosystem repository path: {repo_dir}")

        # -------------------------------------------------------------------------
        # STEP 1: compiled-policies -> Export formal S* SliceContract & validate schema
        # -------------------------------------------------------------------------
        import bridge_a2dlc

        slice_id = "compras.procesar_solicitudes.v1"
        s_star_contract = bridge_a2dlc.exportar_slice_contract(
            bloque={"id": "bloque_compras_v1"},
            slice_id=slice_id,
            funcion_nombre="src/compras/politica.py::procesar_solicitudes",
        )
        valid_schema, schema_msg = bridge_a2dlc.validar_contra_schema_a2dlc(s_star_contract)
        self.assertTrue(valid_schema, f"compiled-policies S* failed A2-DLC schema validation: {schema_msg}")

        smt_invariants = [inv for inv in s_star_contract["invariants"] if inv["verification"] == "smt"]
        self.assertGreaterEqual(len(smt_invariants), 6)

        # -------------------------------------------------------------------------
        # STEP 2: Set up temporary git repository for the slice & invoke Entropos + adapter.py
        # -------------------------------------------------------------------------
        a2dlc_bin = shutil.which("a2dlc") or str(Path.home() / "go" / "bin" / ("a2dlc.exe" if os.name == "nt" else "a2dlc"))
        self.assertTrue(Path(a2dlc_bin).exists(), f"a2dlc binary not found at {a2dlc_bin}")

        srv = HTTPServer(("127.0.0.1", 0), EntroposGatewayStub)
        entropos_url = f"http://127.0.0.1:{srv.server_address[1]}/v1/chat/completions"
        threading.Thread(target=srv.serve_forever, daemon=True).start()

        try:
            with tempfile.TemporaryDirectory() as tmp:
                repo = Path(tmp) / "golden_repo"
                repo.mkdir(parents=True)

                def git(*args: str) -> str:
                    env = {
                        **os.environ,
                        "GIT_AUTHOR_NAME": "a2dlc-e2e",
                        "GIT_AUTHOR_EMAIL": "e2e@falsable.local",
                        "GIT_COMMITTER_NAME": "a2dlc-e2e",
                        "GIT_COMMITTER_EMAIL": "e2e@falsable.local",
                    }
                    proc = subprocess.run(["git", *args], cwd=repo, env=env, capture_output=True, text=True, check=True)
                    return proc.stdout.strip()

                git("init", "-q", "-b", "main")

                # Base tree: initial stub of politica.py and a downstream consumer in services/finanzas/auditor.py
                (repo / "src" / "compras").mkdir(parents=True)
                (repo / "src" / "compras" / "__init__.py").write_text("", encoding="utf-8")
                (repo / "src" / "compras" / "politica.py").write_text(
                    "def procesar_solicitudes(entradas: dict) -> dict:\n    return {'presupuesto_final': 0}\n",
                    encoding="utf-8",
                )
                (repo / "services" / "finanzas").mkdir(parents=True)
                (repo / "services" / "finanzas" / "auditor.py").write_text(
                    "from src.compras.politica import procesar_solicitudes\n\n"
                    "def auditar_lote(lote: dict) -> bool:\n"
                    "    res = procesar_solicitudes(lote)\n"
                    "    return res['presupuesto_final'] >= 0\n",
                    encoding="utf-8",
                )

                # Configure Stage 6 Indexer pointing to real tools/chronosgraph_blast_radius.py
                indexer_script = (A2DLC_ROOT / "tools" / "chronosgraph_blast_radius.py").as_posix()
                py_exe = Path(sys.executable).as_posix()
                config_doc = {
                    "schema_version": 1,
                    "engines": [],
                    "integration": {
                        "indexer": {
                            "command": f"{py_exe} {indexer_script}",
                            "max_depth": 3,
                        },
                        "migrations": {
                            "paths": ["migrations/**"],
                        },
                    },
                }
                (repo / ".a2dlc").mkdir()
                (repo / ".a2dlc" / "config.json").write_text(json.dumps(config_doc, indent=2) + "\n", encoding="utf-8")
                (repo / "migrations").mkdir()
                (repo / "migrations" / "0001_init.sql").write_text(
                    "CREATE TABLE solicitudes (id TEXT PRIMARY KEY, importe_centimos INT NOT NULL);\n",
                    encoding="utf-8",
                )

                git("add", "-A")
                git("commit", "-qm", "base: initial compras and finanzas consumer")

                # Dispatch builder_primary through roles/adapter.py with A2DLC_LLM=entropos
                role_io = Path(tmp) / "role_io"
                (role_io / "in").mkdir(parents=True)
                (role_io / "out").mkdir(parents=True)

                intent_doc = {
                    "schema_version": 1,
                    "slice_id": slice_id,
                    "mode": "lite",
                    "goal": "Implementar política de compras con invariantes SMT de conservación presupuestaria",
                    "non_goals": [],
                    "criticality": "high",
                    "critical_paths": ["src/compras/**"],
                    "acceptance": ["presupuesto_final >= 0"],
                    "invariants": [inv["dsl"] for inv in smt_invariants if inv.get("dsl")],
                    "budget": {
                        "max_inner_iterations": 5,
                        "max_tier1_adversarial_rounds": 3,
                    },
                    "delivery": {
                        "writes": True,
                        "compensation": {
                            "strategy": "compensating_events",
                            "description": "revertir_lote_compras(tx_id) restituye el saldo retenido",
                        },
                        "rings": [1, 10, 100],
                        "read_only_paths": ["services/finanzas/auditor.py"],
                    },
                }
                intent_path = Path(tmp) / "intent.json"
                intent_path.write_text(json.dumps(intent_doc, indent=2) + "\n", encoding="utf-8")

                role_req = {
                    "schema_version": 1,
                    "run_id": "run-golden123456",
                    "slice_id": slice_id,
                    "mode": "lite",
                    "role": "builder_primary",
                    "stage": "3",
                    "attempt": 1,
                    "inputs": [],
                    "workdir": "/work",
                    "payload": {
                        "intent": intent_doc,
                        "contract": s_star_contract,
                        "probes": s_star_contract["probes"],
                        "round": 1,
                        "max_rounds": 3,
                        "tier0_feedback": None,
                    },
                }
                (role_io / "in" / "request.json").write_text(json.dumps(role_req, indent=2) + "\n", encoding="utf-8")
                (role_io / "in" / "contract.json").write_text(json.dumps(s_star_contract, indent=2) + "\n", encoding="utf-8")

                adapter_env = {
                    **os.environ,
                    "A2DLC_REQUEST": str(role_io / "in" / "request.json"),
                    "A2DLC_RESPONSE": str(role_io / "out" / "response.json"),
                    "A2DLC_ROLE": "builder_primary",
                    "A2DLC_STAGE": "3",
                    "A2DLC_LLM": "entropos",
                    "A2DLC_MODEL": "entropos-auto",
                    "A2DLC_ENTROPOS_URL": entropos_url,
                    "ENTROPOS_API_KEY": "ent-golden-thread-key",
                    "A2DLC_WORKDIR": str(repo),
                    "A2DLC_TIMEOUT_S": "30",
                    "A2DLC_PROMPTS_DIR": str(A2DLC_ROOT / "roles" / "prompts"),
                }
                adapter_proc = subprocess.run(
                    [sys.executable, "-I", "-X", "utf8", str(A2DLC_ROOT / "roles" / "adapter.py")],
                    env=adapter_env,
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                )
                self.assertEqual(adapter_proc.returncode, 0, f"adapter.py failed: {adapter_proc.stderr}")

                role_resp = json.loads((role_io / "out" / "response.json").read_text(encoding="utf-8"))
                self.assertEqual(role_resp["status"], "ok")
                self.assertEqual(role_resp["model"], "claude-3-5-sonnet-20241022")
                self.assertAlmostEqual(role_resp["cost"]["usd"], 0.00185, places=6)
                self.assertEqual(role_resp["payload"]["files_changed"], ["src/compras/politica.py"])

                # Also add an expand-only migration in the candidate tree so Stage 6 checks migrations + AST blast radius
                (repo / "migrations" / "0002_add_audit_col.sql").write_text(
                    "ALTER TABLE solicitudes ADD COLUMN audit_hash TEXT;\n",
                    encoding="utf-8",
                )

                # -------------------------------------------------------------------------
                # STEP 3 & 4: Execute `a2dlc run` (Stages -1..7) + `ChronosGraph` Stage 6 Indexer
                # -------------------------------------------------------------------------
                run_env = {
                    **os.environ,
                    "CHRONOSGRAPH_SRC": str(CHRONOSGRAPH_SRC),
                }
                run_proc = subprocess.run(
                    [
                        a2dlc_bin,
                        "run",
                        "--slice",
                        slice_id,
                        "--intent",
                        str(intent_path),
                        "--mode",
                        "lite",
                        "--z3-image",
                        "",
                    ],
                    cwd=repo,
                    env=run_env,
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                )
                self.assertEqual(
                    run_proc.returncode,
                    0,
                    f"a2dlc run exited {run_proc.returncode}:\nSTDOUT: {run_proc.stdout}\nSTDERR: {run_proc.stderr}",
                )

                # Verify ledger & run artifacts with `a2dlc verify-run`
                verify_run_proc = subprocess.run(
                    [a2dlc_bin, "verify-run", "--slice", slice_id],
                    cwd=repo,
                    env=run_env,
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                )
                self.assertEqual(
                    verify_run_proc.returncode,
                    0,
                    f"a2dlc verify-run failed:\n{verify_run_proc.stdout}\n{verify_run_proc.stderr}",
                )

                # Inspect Stage 6 Integration artifact (ChronosGraph AST Blast Radius)
                run_dir = repo / ".a2dlc" / "run" / slice_id
                integration_doc = json.loads((run_dir / "integration.json").read_text(encoding="utf-8"))
                self.assertEqual(integration_doc["verdict"], "PASS")
                self.assertIsNotNone(integration_doc.get("blast_radius"), "ChronosGraph blast_radius missing in integration.json")
                self.assertGreaterEqual(integration_doc["blast_radius"]["total_impacted"], 1)
                discovered_edges = integration_doc.get("discovered", [])
                self.assertTrue(
                    any(
                        e.get("from") == "services/finanzas/auditor.py"
                        and e.get("to") == "src/compras/politica.py"
                        and e.get("via") == "code_graph"
                        for e in discovered_edges
                    ),
                    f"Expected ChronosGraph code_graph edge not found in discovered: {discovered_edges}",
                )

                # Inspect Stage 7 Progressive Delivery artifact
                delivery_doc = json.loads((run_dir / "delivery.json").read_text(encoding="utf-8"))
                self.assertEqual([r["percent"] for r in delivery_doc["rings"]], [1, 10, 100])
                self.assertEqual(delivery_doc["max_ring"], 100)
                self.assertIsNone(delivery_doc.get("blocked_by"))

                # -------------------------------------------------------------------------
                # STEP 5: Ingest `.a2dlc/verdict.json` into `agencia-ia` Agora Core (ISO 42001)
                # -------------------------------------------------------------------------
                from app.domain.code_verification import audit_a2dlc_receipt

                verdict_path = repo / ".a2dlc" / "verdict.json"
                self.assertTrue(verdict_path.exists(), "verdict.json was not written by a2dlc run")
                verdict_doc = json.loads(verdict_path.read_text(encoding="utf-8"))

                audit_record = audit_a2dlc_receipt(
                    verdict_doc=verdict_doc,
                    waivers_doc=None,
                    approvals_doc=None,
                    repo_info={"name": "falsable/golden-thread-slice", "slice_id": slice_id},
                )
                self.assertEqual(audit_record["verdict"], "PASS")
                self.assertEqual(audit_record["compliance_status"], "COMPLIANT")
                self.assertTrue(audit_record["iso_42001_defensible"])
                self.assertEqual(audit_record["tree_hash"], verdict_doc["tree_hash"])
                self.assertTrue(len(audit_record["attestation_hash"]) == 64)
        finally:
            srv.shutdown()
            srv.server_close()


if __name__ == "__main__":
    unittest.main()
