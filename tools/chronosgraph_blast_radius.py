#!/usr/bin/env python3
"""
chronosgraph_blast_radius.py — ChronosGraph Code Graph Indexer Bridge for A2-DLC Stage 6.

Conforms to A2-DLC protocol §5 (Integration) and contracts/indexer-response.schema.json.
Given a candidate tree and changed files, computes:
1. Code graph dependency edges (importers of changed files).
2. Downstream blast radius (direct and transitive dependents up to max_depth).
3. AST side-effect risk score and high-risk function count via ChronosGraph analyzers.
"""

from __future__ import annotations

import json
import os
import sys
from collections import deque
from pathlib import Path
from typing import Any

def _locate_chronos_src() -> Path:
    env_src = os.environ.get("CHRONOSGRAPH_SRC")
    if env_src:
        return Path(env_src)
    # Default to sibling repository in the workspace (../../ChronosGraph/src or ../../chronosgraph/src)
    repo_root = Path(__file__).resolve().parent.parent
    for name in ("ChronosGraph", "chronosgraph"):
        candidate = repo_root.parent / name / "src"
        if candidate.exists():
            return candidate
    return repo_root.parent / "ChronosGraph" / "src"


CHRONOS_SRC = _locate_chronos_src()
if CHRONOS_SRC.exists() and str(CHRONOS_SRC) not in sys.path:
    sys.path.insert(0, str(CHRONOS_SRC))



def run_blast_radius(cand_dir: str, changed_files: list[str], max_depth: int = 3) -> dict[str, Any]:
    cand_path = Path(cand_dir).resolve()
    if not changed_files:
        return {
            "schema_version": 1,
            "edges": [],
            "blast_radius": {
                "total_impacted": 0,
                "ranking": [],
            },
            "error": None,
        }

    try:
        from chronosgraph.context_engineering.analyzers.ast_analyzer import TreeSitterAnalyzer
        from chronosgraph.context_engineering.analyzers.side_effects_analyzer import SideEffectsAnalyzer

        analyzer = TreeSitterAnalyzer()
        sea = SideEffectsAnalyzer()

        # Parse candidate files and build dependency graph (imported_by map)
        parsed_files = analyzer.analyze_repo(cand_path)

        # Build symbol & module lookup tables
        imported_by: dict[str, set[str]] = {}
        file_to_module: dict[str, str] = {}
        module_to_file: dict[str, str] = {}

        for item in parsed_files:
            rel = Path(item["file"]).relative_to(cand_path).as_posix()
            mod_name = rel.replace("/", ".").removesuffix(".py").removesuffix(".ts").removesuffix(".js")
            file_to_module[rel] = mod_name
            module_to_file[mod_name] = rel

        for item in parsed_files:
            importer_rel = Path(item["file"]).relative_to(cand_path).as_posix()
            imports = item.get("imports", [])
            for imp in imports:
                target_rel = None
                if imp in file_to_module:
                    target_rel = imp
                elif imp in module_to_file:
                    target_rel = module_to_file[imp]
                else:
                    for m, f in module_to_file.items():
                        if not m:
                            continue
                        if m in imp or m.endswith(imp) or imp.endswith(m):
                            target_rel = f
                            break

                if target_rel and target_rel != importer_rel:
                    imported_by.setdefault(target_rel, set()).add(importer_rel)

        # Calculate blast radius and edges for each changed file
        edges: list[dict[str, str]] = []
        ranking: list[dict[str, Any]] = []
        all_impacted: set[str] = set()

        changed_set = set(changed_files)
        for cf in sorted(changed_files):
            norm_cf = Path(cf).as_posix()
            visited: dict[str, int] = {}
            queue: deque[tuple[str, int]] = deque([(norm_cf, 0)])

            while queue:
                curr, depth = queue.popleft()
                if curr in visited or depth > max_depth:
                    continue
                visited[curr] = depth

                direct_consumers = imported_by.get(curr, set())
                for cons in direct_consumers:
                    if cons not in visited:
                        queue.append((cons, depth + 1))
                        if depth == 0 and norm_cf in changed_set:
                            edges.append({"from": cons, "to": norm_cf})

            impacted_for_file = {f for f, d in visited.items() if f != norm_cf and d > 0}
            all_impacted.update(impacted_for_file)

            # Side effects / risk score
            abs_file = cand_path / norm_cf
            risk_score = 0.0
            high_risk_count = 0
            if abs_file.exists():
                try:
                    sea_res = sea.analyze_file(str(abs_file))
                    high_risk_count = sea_res.get("high_risk_count", 0)
                    functions = sea_res.get("functions", [])
                    risk_score = max((f.get("risk_score", 0.0) for f in functions), default=0.0)
                except Exception:
                    pass

            ranking.append({
                "file": norm_cf,
                "blast_radius": len(impacted_for_file),
                "risk_score": round(risk_score, 3),
                "high_risk_functions": high_risk_count,
            })

        # Sort ranking descending by blast_radius then risk_score
        ranking.sort(key=lambda x: (x["blast_radius"], x["risk_score"]), reverse=True)

        return {
            "schema_version": 1,
            "edges": edges,
            "blast_radius": {
                "total_impacted": len(all_impacted),
                "ranking": ranking,
            },
            "error": None,
        }
    except Exception as e:
        return {
            "schema_version": 1,
            "edges": [],
            "blast_radius": {
                "total_impacted": 0,
                "ranking": [],
            },
            "error": str(e),
        }


def main():
    io_dir = os.environ.get("A2DLC_IO")
    req = {}
    if io_dir and (Path(io_dir) / "request.json").exists():
        req = json.loads((Path(io_dir) / "request.json").read_text(encoding="utf-8"))
    elif len(sys.argv) > 1 and Path(sys.argv[1]).exists():
        req = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))

    cand_dir = req.get("candidate_dir") or os.environ.get("A2DLC_CAND_DIR") or "."
    changed_str = os.environ.get("A2DLC_CHANGED", "")
    changed = req.get("changed_files") or ([f for f in changed_str.split(" ") if f] if changed_str else [])
    max_depth = int(req.get("max_depth") or os.environ.get("A2DLC_MAX_DEPTH") or 3)

    resp = run_blast_radius(cand_dir, changed, max_depth=max_depth)
    resp_bytes = json.dumps(resp, indent=2)

    if io_dir:
        (Path(io_dir) / "response.json").write_text(resp_bytes, encoding="utf-8")

    print(resp_bytes)


if __name__ == "__main__":
    main()
