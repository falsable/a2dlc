"""RFC-versus-contract check: section 6.4 of the specification (the receipt) must not contradict the schemas or the code
it describes. It reads the RFC, contracts/*.schema.json and internal/gitx/gitx.go, and exits 1 listing every
disagreement, 0 if there is none.
Usage (jsonschema is not installed on the host; run it in the contract-test image, like validate.py):
  docker run --rm -v "<repo>:/r:ro" a2dlc-contract-test python /r/contracts/tests/check_rfc.py /r"""
import json
import re
import sys
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

RFC = "docs/A2-DLC_v3.5_protocol.md"
GITX = "internal/gitx/gitx.go"
# Paths older versions of the specification used. They may be named only where the text says what changed.
HISTORICAL = {".a2dlc/latest.json", ".a2dlc/waivers.yaml", ".a2dlc/flaky.yaml"}
HISTORY_SECTIONS = ("#### 6.4.10", "## 13.")
# The rows of the WaiverSHA escape table (6.4.6) that Go's encoding/json writes as \u escapes. Built from chr(92) so that no
# tool between the editor and the file turns the escapes back into the characters they stand for.
_BS = chr(92)
WAIVER_ESCAPES = {
    "`<`, `>`, `&`": ", ".join(f"`{_BS}u{h}`" for h in ("003c", "003e", "0026")),
    "U+2028, U+2029": ", ".join(f"`{_BS}u{h}`" for h in ("2028", "2029")),
}


def sections(text):
    """(heading line, body lines) for every heading of level 2 to 4, in order."""
    out, cur = [], ("", [])
    for line in text.split("\n"):
        if re.match(r"^#{2,4} ", line):
            out.append(cur)
            cur = (line, [])
        else:
            cur[1].append(line)
    out.append(cur)
    return out


def section_64(text):
    """The text from '### 6.4 ' to the next '## ' heading, or None."""
    m = re.search(r"^### 6\.4 .*$", text, re.M)
    if not m:
        return None
    rest = text[m.start():]
    n = re.search(r"^## ", rest, re.M)
    return rest[: n.start()] if n else rest


def load_schemas(contracts):
    schemas = {p.name.removesuffix(".schema.json"): json.loads(p.read_text(encoding="utf-8")) for p in contracts.glob("*.schema.json")}
    registry = Registry().with_resources((s["$id"], Resource.from_contents(s)) for s in schemas.values())
    return schemas, registry


def tagged_blocks(sec):
    """Fenced blocks of the section as (info string, tag or None, body)."""
    blocks = []
    lines = sec.split("\n")
    i = 0
    while i < len(lines):
        m = re.match(r"^```(\w*)\s*$", lines[i])
        if m:
            j = i + 1
            while j < len(lines) and not lines[j].startswith("```"):
                j += 1
            tag = None
            k = i - 1
            if k >= 0 and (t := re.match(r"^<!-- (a2dlc:[\w: -]+) -->$", lines[k].strip())):
                tag = t.group(1)
            blocks.append((m.group(1), tag, "\n".join(lines[i + 1 : j])))
            i = j
        i += 1
    return blocks


def table_after(sec, tag):
    """Rows (cells) of the markdown table that follows the comment `<!-- tag -->`."""
    lines = sec.split("\n")
    for i, line in enumerate(lines):
        if line.strip() == f"<!-- {tag} -->":
            rows = []
            for row in lines[i + 1 :]:
                if not row.startswith("|"):
                    break
                cells = [c.strip() for c in row.strip().strip("|").split("|")]
                if set("".join(cells)) <= set("-: "):
                    continue
                rows.append(cells)
            return rows[1:]
    return None


def excluded_in_code(gitx_text):
    m = re.search(r"var Excluded = \[\]string\{([^}]*)\}", gitx_text)
    return re.findall(r'"([^"]+)"', m.group(1)) if m else None


def check(rfc_text, contracts, gitx_text):
    errs = []
    schemas, registry = load_schemas(contracts)
    sec = section_64(rfc_text)
    if sec is None:
        return ["the RFC has no section '### 6.4 '"]

    # 1. Every JSON block of 6.4 names its schema and validates against it.
    for info, tag, body in tagged_blocks(sec):
        if info != "json":
            continue
        m = re.fullmatch(r"a2dlc:schema (\S+)", tag or "")
        if not m:
            errs.append("a ```json block in 6.4 has no '<!-- a2dlc:schema NAME -->' line before it: " + body.strip().split("\n")[0][:60])
            continue
        if m.group(1) not in schemas:
            errs.append(f"a JSON block names the schema '{m.group(1)}', which does not exist in contracts/")
            continue
        try:
            doc = json.loads(body)
        except ValueError as e:
            errs.append(f"the '{m.group(1)}' example is not JSON: {e}")
            continue
        v = Draft202012Validator(schemas[m.group(1)], registry=registry, format_checker=FormatChecker())
        for e in list(v.iter_errors(doc))[:3]:
            errs.append(f"the '{m.group(1)}' example does not validate: {e.message[:140]}")

    # 2. Every .a2dlc/ path the RFC names is one the schemas name (or a historical one, in the sections that say so).
    known = set()
    for p in contracts.glob("*.schema.json"):
        known |= {x.rstrip("./") for x in re.findall(r"\.a2dlc/[\w./<>-]+", p.read_text(encoding="utf-8"))}
    known |= {".a2dlc"} | set(excluded_in_code(gitx_text) or [])
    # The paths a block lists as 'not excluded' are examples of what is not a contract path; the next check tests them.
    scanned = re.sub(r"<!-- a2dlc:not-excluded -->\n```text\n.*?\n```", "", rfc_text, flags=re.S)
    for heading, body in sections(scanned):
        for n, line in enumerate(body, 1):
            for path in re.findall(r"\.a2dlc/[\w./<>-]*[\w>]", line):
                if path in HISTORICAL:
                    if not heading.startswith(HISTORY_SECTIONS):
                        errs.append(f"'{path}' is a name older versions used; it may appear only in 6.4.10 and the history ({heading.strip()[:40]})")
                elif not any(path == k or k.startswith(path + "/") or path.startswith(k + "/") and k.endswith(">") for k in known):
                    errs.append(f"the RFC names '{path}', which no schema in contracts/ names ({heading.strip()[:40]})")

    # 3. The excluded paths are the ones the code excludes.
    code = excluded_in_code(gitx_text)
    blocks = [b for b in tagged_blocks(sec) if b[1] == "a2dlc:excluded"]
    if code is None:
        errs.append(f"cannot read 'var Excluded' in {GITX}")
    elif len(blocks) != 1:
        errs.append("6.4 needs exactly one '<!-- a2dlc:excluded -->' block listing the excluded paths")
    else:
        rfc = [x for x in blocks[0][2].split("\n") if x.strip()]
        if rfc != code:
            errs.append(f"the excluded paths in the RFC {rfc} differ from {GITX} {code}")

    # 4. The field table is the verdict schema's properties, and 'Required' is its required list.
    vs = schemas["verdict"]
    rows = table_after(sec, "a2dlc:fields verdict")
    if rows is None:
        errs.append("6.4 needs the '<!-- a2dlc:fields verdict -->' table")
    else:
        got = {r[0].strip("`"): r[1] for r in rows}
        want = set(vs["properties"])
        if set(got) != want:
            errs.append(f"the field table differs from verdict.schema.json: only in RFC {sorted(set(got) - want)}, only in schema {sorted(want - set(got))}")
        for name, req in got.items():
            if name in want and (req == "yes") != (name in vs["required"]):
                errs.append(f"field '{name}' is '{req}' in the RFC and {'required' if name in vs['required'] else 'optional'} in the schema")

    # 4b. The paths the RFC calls 'not excluded' are not excluded by the rule it states (exact, or under it at a '/').
    nb = [b for b in tagged_blocks(sec) if b[1] == "a2dlc:not-excluded"]
    if code is not None and len(nb) != 1:
        errs.append("6.4 needs exactly one '<!-- a2dlc:not-excluded -->' block")
    elif code is not None:
        for path in [x for x in nb[0][2].split("\n") if x.strip()]:
            if any(path == e or path.startswith(e.rstrip("/") + "/") for e in code):
                errs.append(f"'{path}' is listed as not excluded but the excluded paths cover it")

    # 5. The files table says which paths are outside the hash: exactly the excluded ones.
    rows = table_after(sec, "a2dlc:files")
    if rows is None:
        errs.append("6.4 needs the '<!-- a2dlc:files -->' table")
    elif code is not None:
        outside = {r[0].strip("`").rstrip("/") for r in rows if r[2] == "no"}
        if outside != {c.rstrip("/") for c in code}:
            errs.append(f"the files table marks {sorted(outside)} as outside the hash; the code excludes {sorted(code)}")

    # 6. Literal facts that a schema states.
    pat = vs["properties"]["tree_hash"]["pattern"]
    if pat not in sec:
        errs.append(f"6.4 does not quote the tree_hash pattern of the schema: {pat}")
    desc = schemas["approvals"]["description"]
    signed = re.search(r'"(a2dlc-approval/1\\n[^"]*)"', desc)
    ns = re.search(r"namespace (\S+?)\)", desc)
    if not signed or signed.group(1) not in sec:
        errs.append("6.4 does not give the signed text of approvals.schema.json: " + (signed.group(1) if signed else "?"))
    if not ns or ns.group(1) not in sec:
        errs.append("6.4 does not give the namespace of approvals.schema.json")
    engines = schemas["defs"]["$defs"]["target_result"]["properties"]["engine"]["enum"]
    m = re.search(r"lists today \(([^)]*)\)", sec)
    if not m or set(re.findall(r"`([^`]+)`", m.group(1))) != set(engines):
        errs.append(f"6.4 lists the engines as {m.group(1) if m else 'nothing'}; the schema has {engines}")
    for d in schemas["waivers"]["properties"]["waivers"]["items"]["properties"]["decision"]["enum"]:
        if f"`{d}`" not in sec:
            errs.append(f"6.4 does not mention the waiver decision '{d}'")
    for n in vs["properties"]["targets"]["items"]["properties"]["note"]["enum"]:
        if f"`{n}`" not in sec and f'"{n}"' not in sec:
            errs.append(f"6.4 does not mention the note '{n}'")
    for v in schemas["defs"]["$defs"]["verdict"]["enum"]:
        if f"`{v}`" not in sec:
            errs.append(f"6.4 does not mention the verdict value '{v}'")

    # 8. The WaiverSHA escape table writes the escapes out. Where the right column repeated the left one, a reader who followed
    # the table hashed <, >, & and U+2028/U+2029 raw and got another WaiverSHA (bead a2dlc-qhw).
    rows = table_after(sec, "a2dlc:waiver-escapes")
    if rows is None:
        errs.append("6.4 needs the '<!-- a2dlc:waiver-escapes -->' table")
    else:
        got = {r[0]: r[1] for r in rows if len(r) == 2}
        for left, right in WAIVER_ESCAPES.items():
            if got.get(left) != right:
                errs.append(f"the WaiverSHA escape table writes {left} as {got.get(left)!r}, not {right!r}")
    return errs


def main(argv):
    root = Path(argv[1] if len(argv) > 1 else Path(__file__).resolve().parents[2])
    rfc = Path(argv[2]) if len(argv) > 2 else root / RFC
    errs = check(rfc.read_text(encoding="utf-8"), root / "contracts", (root / GITX).read_text(encoding="utf-8"))
    for e in errs:
        print("BAD", e)
    print(f"\n{len(errs)} disagreements between {rfc.name} and the contract")
    return 1 if errs else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
