"""Normalizes a gates fixture manifest to the English schema that e2e/judge_gates.py reads.
The frozen gates-py manifest (casos.json) uses Spanish keys; gates-prop (cases.json) already uses the English ones.
Usage: python e2e/normalize_manifest.py <source manifest.json> <destination manifest.json>
Unknown keys pass through unchanged, so a typo in a new fixture shows up as a missing key in the judge, not as a
silent default."""
import json
import sys

TOP = {"casos": "cases", "imagen": "image"}
CASE = {"casos": "cases", "esperado": "expected", "que": "why", "archivos": "files", "imagen": "image",
        "no_juzgados": "not_judged", "waivers_pendientes": "pending_waivers"}


def normalize(m):
    """Returns the English-keyed copy of a fixture manifest. Keys already in English are left as they are."""
    out = {TOP.get(k, k): v for k, v in m.items()}
    out["cases"] = [{CASE.get(k, k): v for k, v in c.items()} for c in out.get("cases", [])]
    return out


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as src:
        manifest = normalize(json.load(src))
    with open(sys.argv[2], "w", encoding="utf-8") as dst:
        json.dump(manifest, dst, indent=1, ensure_ascii=False)
