# Quickstart: the refactor that lied

`./demo.sh` builds a throwaway repo, commits a `apply_discount` that clamps negative totals to 0, then applies a "simplification" that drops the clamp. The unit tests never cover `discount > price`, so they stay green. `a2dlc check` runs CrossHair `diffbehavior` on the changed function inside a `--network none` container and returns the input that separates old from new: `args=price=-1, discount=0 old=returns 0 new=returns -1`. The demo's base config lists `src/pricing.py` under `critical_code`: without it, a divergence is a note in the receipt and the verdict stays PASS. The fix restores the clamp and the gate passes.

Record it for the README with [vhs](https://github.com/charmbracelet/vhs): `vhs demo.tape` (tape not included yet).
