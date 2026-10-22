"""Generates and freezes the core's mutants to measure the black-box engine (step 4). It runs ONCE, before writing the
engine; mutantes.json stores the sha256 of each file and measure_bb.sh refuses to run if it does not match.
Usage: python e2e/gen_mutants_bb.py  (writes e2e/testdata/mutantes-bb/)"""
import hashlib
import json
import sys
from pathlib import Path

R = Path(__file__).resolve().parent.parent
FILE = "internal/verdict/verdict.go"
SOURCE = (R / FILE).read_text(encoding="utf-8")

MUTANTS = [
    ("B1-waiver-cualquiera", "FAIL", "the waiver covers any verdict, not only FAIL",
     "w := t.Verdict == contract.FAIL && waived[t.Target]", "w := waived[t.Target]"),
    ("B2-motores-caidos", "FAIL", "a single crashed engine no longer gives INCONCLUSIVE",
     "fail, inc := false, engineErrors > 0", "fail, inc := false, engineErrors > 1"),
    ("B3-exit-inconcluso", "FAIL", "INCONCLUSIVE exits 1 instead of 2",
     "\tcase contract.FAIL:\n\t\treturn 1", "\tcase contract.FAIL, contract.INCONCLUSIVE:\n\t\treturn 1"),
    ("B4-estrella-cruza", "FAIL", "* crosses folders",
     "\t\tcase c == '*':\n\t\t\tb.WriteString(\"[^/]*\")", "\t\tcase c == '*':\n\t\t\tb.WriteString(\".*\")"),
    ("B5-asigna-a-todos", "FAIL", "a file goes to every engine whose glob matches, not only the first",
     "\t\t\tif asignado {\n\t\t\t\tbreak\n\t\t\t}\n", ""),
    ("E1-exitcode-ifs", "PASS", "equivalent refactor of ExitCode",
     "\tswitch v {\n\tcase contract.PASS:\n\t\treturn 0\n\tcase contract.FAIL:\n\t\treturn 1\n\t}\n\treturn 2",
     "\tif v == contract.PASS {\n\t\treturn 0\n\t}\n\tif v == contract.FAIL {\n\t\treturn 1\n\t}\n\treturn 2"),
]

dest = R / "e2e" / "testdata" / "mutantes-bb"
if (dest / "mutantes.json").exists():
    sys.exit("already frozen: not regenerated")
manifest = []
for mid, esperado, description, viejo, nuevo in MUTANTS:
    assert SOURCE.count(viejo) == 1, (mid, SOURCE.count(viejo))
    texto = SOURCE.replace(viejo, nuevo)
    f = dest / mid / FILE
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_bytes(texto.encode("utf-8"))
    manifest.append({"id": mid, "archivo": FILE, "esperado": esperado, "que": description,
                       "sha256": hashlib.sha256(texto.encode("utf-8")).hexdigest()})
source_sha = hashlib.sha256(SOURCE.encode("utf-8")).hexdigest()
(dest / "mutantes.json").write_text(json.dumps({"base_sha256": source_sha, "mutantes": manifest}, ensure_ascii=False, indent=1) + "\n",
                                    encoding="utf-8")
print(len(manifest), "mutants in", dest)
