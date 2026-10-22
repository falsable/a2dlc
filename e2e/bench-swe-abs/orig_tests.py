"""T_orig of VARA-2: the test files touched by SWE-bench's original_test_patch, per instance.
Runs in bench-harness (pyarrow). Usage: orig_tests.py <downloads> <output> <instance_id>...  →  <output>/<id>/orig_tests.txt"""
import os
import re
import sys

import pyarrow.parquet as pq

D, OUT, ids = sys.argv[1], sys.argv[2], sys.argv[3:]
t = pq.read_table(f"{D}/abs.parquet", columns=["instance_id", "original_test_patch"]).to_pylist()
m = {r["instance_id"]: r["original_test_patch"] for r in t}
for i in ids:
    files = sorted(set(re.findall(r"^\+\+\+ b/(\S+)", m[i], re.M)))
    os.makedirs(f"{OUT}/{i}", exist_ok=True)
    open(f"{OUT}/{i}/orig_tests.txt", "w", encoding="utf-8", newline="\n").write("\n".join(files) + "\n")
    print(i, files)
