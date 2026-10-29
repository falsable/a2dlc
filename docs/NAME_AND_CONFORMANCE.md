# Use of the name, and conformance claims

## The name

"A2-DLC" and "a2dlc" name the protocol specified in [RFC-0001](../rfcs/0001-a2dlc-protocol.md) and the reference implementation in this repository, both published by [falsable](https://github.com/falsable) under Apache-2.0. This repository records no trademark registration. Section 6 of the [LICENSE](../LICENSE) grants no rights to use the licensor's names; what follows is the maintainer's request on how to use them.

**Free to do, without asking:**

- Write "A2-DLC" or "a2dlc" to refer to the protocol or to this project, in documentation, articles and talks.
- Say that your product supports, reads or writes A2-DLC documents, with the RFC version and `schema_version` it handles.
- Fork the code under the license. A fork that keeps the behavior of the verdict (what a `PASS`, `FAIL` and `INCONCLUSIVE` mean) may keep calling the binary `a2dlc` while it says it is a fork and names its origin.

**Please do not:**

- Give the name `a2dlc` or `A2-DLC` to a tool that produces verdicts with different semantics than the RFC defines. A `PASS` that means something else under the same name misleads whoever reads the receipt. Pick another name and say which RFC version it is based on.
- Suggest that the maintainer or this project endorses, certifies or maintains your product when they have not said so.

If a use looks wrong, open an issue. The project's remedy is to ask for a correction and to say publicly what it asked; it has no other enforcement.

## Conformance

**No one certifies anything.** The repository has a conformance kit, [`conformance/`](../conformance/README.md), so an implementation can be run against it and a third party can repeat the run. Without such a run, nobody can truthfully say that an implementation "conforms" to RFC-0001. What can be said without a run, and only if true:

> Implements A2-DLC RFC-0001 v3.5, `schema_version` 1. Not tested against a conformance suite.

Do not use "certified", "compliant", "approved" or "official" next to the name. The reference implementation is the source the RFC was written from, so a disagreement between it and the RFC is a bug in one of the two ([rfcs/README.md](../rfcs/README.md#precedence)); it is not a conformance result.

### What the kit covers

231 vectors in three classes, written against RFC-0001 v3.5 (mainly §6.4, the receipt):

- **verifier**: decides whether a receipt is valid for a commit (tree hash, stale and missing receipts, notes and `critical_code`, waivers and signed approvals). The runner calls `<binary> verify --commit HEAD`.
- **producer**: writes receipts and approvals. The runner calls `<binary> check --base <sha>` and `<binary> approve`.
- **engine**: judges changed files inside a container, through the `/io`, `/base` and `/cand` interface.

To run it (Python 3.9+, git and ssh-keygen; Docker for the producer vectors that run an engine and for the engine class):

    docker build -t a2dlc-engine-fake:0.1 engines/fake
    python conformance/run.py --binary <path to the implementation> --engine-image a2dlc-engine-fake:0.1 --strict

An implementation with its own messages adds `--reasons none` and is judged on valid or invalid only. [`conformance/README.md`](../conformance/README.md) describes the vector format and the options.

### What the kit does not cover

- `V-receipt-007` is a known failure of the reference binary: it accepts a receipt with a required field missing, because it does not enforce `required`, `enum` or `pattern` from the schemas (bead a2dlc-5fq). The runner reports it as an expected failure and flags it when it stops failing.
- The engine class checks the protocol (every changed file is accounted for, the response matches its schema), not the quality of a language engine.
- The `V-gates` vectors mirror the shape and the verdict of the frozen Python gate cases; they do not run the gates.
- A working tree that holds nested untracked repositories is not covered (bead a2dlc-3hd); the vectors hash commits only.
- A verifier is judged on `verify --commit HEAD` in a repository with no remote; the `base_unpushed` problem and the pre-push hook are not tested.

### Claims

A claim of conformance names:

1. the implementation and its version;
2. the RFC version and `schema_version`;
3. the class or classes tested (the kit defines the classes; a claim covers only those that were run);
4. the kit version and the command that was run (the kit has no release number yet: name the commit of this repository it was taken from);
5. where the run's output can be read, so a third party can repeat it.

Such a claim is the implementer's own declaration. The maintainer does not certify it, and may list implementations that publish one; being listed is not an endorsement. A claim is withdrawn or updated when the kit, the RFC or the implementation changes and the run no longer passes.

## Receipts

A tool that writes a `.a2dlc/verdict.json` must have run the gates the RFC requires for that verdict. A receipt written without them is a forgery, whatever tool wrote it; [SECURITY.md](../SECURITY.md) describes what the reference implementation does and does not do about that.
