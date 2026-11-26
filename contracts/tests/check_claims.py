"""CI check for docs/CLAIMS.md against README.md (agencia/a2dlc-pe6.5).

Verifies:
1. `docs/CLAIMS.md` defines all four gates `P1`, `P2`, `P3`, `P4` under `## The four gates`.
2. `## README sentences and the gate behind each` has the required columns (`Gate`, `Status`, `Number`,
   `Reproduce command`, `Date`, `Commit`), with non-empty values for every P1-P4 backed claim.
3. Quantitative benchmark markers in `README.md` (outside fenced code blocks and shields badges) appear
   in the `docs/CLAIMS.md` traceability table.
4. Forbidden unbacked claims listed in `docs/CLAIMS.md` do not appear in `README.md` while their gate
   remains `OPEN`.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

REQUIRED_GATES = ("P1", "P2", "P3", "P4")
REQUIRED_COLUMNS = ("README sentence", "Gate", "Status", "Number", "Reproduce command", "Date", "Commit")

# Matches quantitative figures in README.md prose/tables: ratios (18/18), percentages (48.9%),
# latencies (117.4 s, 84–127 s, 21–26 s), and USD costs ($0.025–$0.069).
QUANT_RE = re.compile(
    r"(?:\b\d+/\d+\b|"
    r"\b\d+(?:\.\d+)?%|"
    r"\$\d+(?:\.\d+)?(?:[–-]\$\d+(?:\.\d+)?)?|"
    r"\b\d+(?:\.\d+)?(?:[–-]\d+(?:\.\d+)?)?\s*s\b)"
)

FORBIDDEN_OPEN_PHRASES = (
    "faster development",
    "increases velocity",
    "agent-native orchestration of the sdlc",
)

# Historical dates or protocol/version identifiers in README prose that are not benchmark measurements.
_NON_BENCHMARK_IGNORE = {
    "2026-11-03",
    "2026-11-07",
}


def _strip_code_and_badges(markdown: str) -> list[tuple[int, str]]:
    """Returns `(line_no, line)` for non-fenced, non-badge lines of Markdown."""
    out: list[tuple[int, str]] = []
    in_fence = False
    for idx, line in enumerate(markdown.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        if "img.shields.io" in line:
            continue
        out.append((idx, line))
    return out


def _section_lines(markdown: str, heading: str) -> list[str]:
    lines = markdown.splitlines()
    collecting = False
    body: list[str] = []
    for line in lines:
        if line.strip() == heading:
            collecting = True
            continue
        if collecting and line.startswith("## "):
            break
        if collecting:
            body.append(line)
    return body


def _parse_markdown_table(section: list[str]) -> tuple[list[str], list[dict[str, str]]]:
    table_lines = [ln.strip() for ln in section if ln.strip().startswith("|")]
    if len(table_lines) < 3:
        return [], []
    headers = [cell.strip() for cell in table_lines[0].strip("|").split("|")]
    rows: list[dict[str, str]] = []
    for raw in table_lines[2:]:
        cells = [cell.strip() for cell in raw.strip("|").split("|")]
        if len(cells) != len(headers):
            continue
        rows.append(dict(zip(headers, cells)))
    return headers, rows


def check_claims(repo_root: Path) -> list[str]:
    """Returns a list of error strings; empty means all checks pass."""
    errors: list[str] = []
    claims_path = repo_root / "docs" / "CLAIMS.md"
    readme_path = repo_root / "README.md"
    if not claims_path.exists():
        return [f"missing {claims_path}"]
    if not readme_path.exists():
        return [f"missing {readme_path}"]

    claims_text = claims_path.read_text(encoding="utf-8")
    readme_text = readme_path.read_text(encoding="utf-8")

    # 1. Verify P1-P4 in ## The four gates
    gates_headers, gates_rows = _parse_markdown_table(_section_lines(claims_text, "## The four gates"))
    if not gates_headers:
        errors.append("docs/CLAIMS.md: missing table under '## The four gates'")
        return errors

    gate_status: dict[str, str] = {}
    for row in gates_rows:
        gate = row.get("Gate", "").strip()
        status = row.get("Status", "").strip().upper()
        if gate:
            gate_status[gate] = status
    for g in REQUIRED_GATES:
        if g not in gate_status:
            errors.append(f"docs/CLAIMS.md: missing gate {g} in '## The four gates'")

    # 2. Verify ## README sentences and the gate behind each
    trace_headers, trace_rows = _parse_markdown_table(
        _section_lines(claims_text, "## README sentences and the gate behind each")
    )
    if trace_headers != list(REQUIRED_COLUMNS):
        errors.append(
            f"docs/CLAIMS.md: traceability table columns must be {list(REQUIRED_COLUMNS)}, got {trace_headers}"
        )
        return errors

    gated_corpus = []
    for i, row in enumerate(trace_rows, start=1):
        gate_val = row["Gate"]
        if any(g in gate_val for g in REQUIRED_GATES):
            for col in ("Status", "Number", "Reproduce command", "Date", "Commit"):
                if not row.get(col) or row[col] in ("—", "-", "TBD"):
                    errors.append(f"docs/CLAIMS.md: row {i} ({gate_val}) has empty {col!r}")
            gated_corpus.append(f"{row['README sentence']} {row['Number']}")
    corpus_text = "\n".join(gated_corpus).lower()

    # 3. Verify every quantitative claim in README.md is covered by docs/CLAIMS.md
    for line_no, line in _strip_code_and_badges(readme_text):
        lower = line.lower()
        for phrase in FORBIDDEN_OPEN_PHRASES:
            if phrase in lower and any(gate_status.get(g) == "OPEN" for g in REQUIRED_GATES):
                errors.append(
                    f"README.md:{line_no}: forbidden unbacked claim phrase {phrase!r} while gates remain OPEN"
                )
        for m in QUANT_RE.finditer(line):
            tok = m.group(0)
            if tok in _NON_BENCHMARK_IGNORE:
                continue
            # Ignore Markdown anchor links like (#...) or prose section/range references when not a benchmark claim
            norm = tok.lower().strip()
            if norm not in corpus_text:
                errors.append(
                    f"README.md:{line_no}: quantitative figure {tok!r} is not recorded in docs/CLAIMS.md"
                )

    return errors


def main(argv: list[str] | None = None) -> int:
    args = argv if argv is not None else sys.argv[1:]
    root = Path(args[0]) if args else Path(".")
    errs = check_claims(root.resolve())
    if errs:
        for e in errs:
            print(f"FAIL: {e}", file=sys.stderr)
        return 1
    print("OK: README.md claims match docs/CLAIMS.md gates P1-P4.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
