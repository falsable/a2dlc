# RFC 0013: Stage −1 names its runs of baseline_command

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §5 Stage −1 (CB-05), RFC-0002
- **Changes `schema_version`:** no

## Problem

Stage −1 runs `tests.baseline_command` twice on the base with `$A2DLC_TESTS` listing every test file of the base that
matches `tests.paths`, and checks that both runs agree (CB-05). A repo whose `baseline_command` runs a selection plus
`$A2DLC_TESTS` (the impacted set of a change, plus the change's tests) therefore runs its whole suite in Stage −1, and the
command cannot tell that run apart from a check whose change only touched tests: both leave `$A2DLC_CHANGED` empty.

Measured on a factory lane over a Python repo with about 4,700 tests: the whole list included two opt-in folders the
repo's pytest configuration ignores, collection aborted on a `conftest` name clash, and each of the two runs took 134 s to
collect the tree under coverage and run no test. CB-05 then held over three module import errors. Fixing the collection
would have made each run the whole suite under coverage instead.

## Proposal

Stage −1's runs get `A2DLC_STAGE=-1` in their environment, and no other run does. A command may read it to run its own
selection on the base (for example its selection for an empty change) instead of every test it is given. When the command
names `A2DLC_STAGE`, the suite cache keys Stage −1's answer apart from a check's answer of the same tree, so one does not
stand in for the other.

## Compatibility

No contract change for a command that does not read the variable: it gets the same list and keeps the same cache key. CB-05
still asks two agreeing runs. What the base run proves is the repo's choice, as it already was through `baseline_command`.
