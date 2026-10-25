# Quickstart: the refactor that lied

`./demo.sh` builds a throwaway repo, commits a `apply_discount` that clamps negative totals to 0, then applies a "simplification" that drops the clamp. The unit tests never cover `discount > price`, so they stay green. `a2dlc check` runs CrossHair `diffbehavior` on the changed function inside a `--network none` container and returns the input that separates old from new: `args=(100, 101) -> old=0, new=-1`. The fix restores the clamp and the gate passes.

Record it for the README with [vhs](https://github.com/charmbracelet/vhs): `vhs demo.tape` (tape not included yet).
