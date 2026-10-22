"""For each requested instance, generates with swebench 4.1.0 the SWE-bench eval.sh over the ABS dataset (augmented
test_patch) and saves spec.json (ABS F2P/P2P, image, test_cmd). Runs inside bench-harness, without network.
Usage: eval_scripts.py <downloads> <output> <instance_id>..."""
import json
import sys

import pandas as pd
from swebench.harness.test_spec.test_spec import make_test_spec

D, OUT, ids = sys.argv[1], sys.argv[2], sys.argv[3:]
df = pd.read_parquet(f"{D}/abs.parquet").set_index("instance_id", drop=False)
for iid in ids:
    r = df.loc[iid]
    inst = {k: (r[k] if k in r else None) for k in
            ["repo", "instance_id", "base_commit", "patch", "test_patch", "problem_statement", "hints_text",
             "created_at", "version", "FAIL_TO_PASS", "PASS_TO_PASS", "environment_setup_commit"]}
    for k in ("FAIL_TO_PASS", "PASS_TO_PASS"):
        if isinstance(inst[k], str):
            inst[k] = json.loads(inst[k])
    inst = {k: ("" if v is None else v) for k, v in inst.items()}
    ts = make_test_spec(inst)
    import os
    os.makedirs(f"{OUT}/{iid}", exist_ok=True)
    open(f"{OUT}/{iid}/eval.sh", "w", encoding="utf-8", newline="\n").write(ts.eval_script)
    open(f"{OUT}/{iid}/gold.diff", "w", encoding="utf-8", newline="\n").write(r["patch"])
    open(f"{OUT}/{iid}/abs_tests.diff", "w", encoding="utf-8", newline="\n").write(r["test_patch"])
    json.dump({"instance_id": iid, "repo": r["repo"], "version": r["version"], "base_commit": r["base_commit"],
               "image": ts.instance_image_key, "FAIL_TO_PASS": inst["FAIL_TO_PASS"], "PASS_TO_PASS": inst["PASS_TO_PASS"]},
              open(f"{OUT}/{iid}/spec.json", "w", encoding="utf-8"), indent=1)
    print(iid, ts.instance_image_key, "f2p", len(inst["FAIL_TO_PASS"]), "p2p", len(inst["PASS_TO_PASS"]))
