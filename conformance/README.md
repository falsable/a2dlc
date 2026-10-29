# a2dlc conformance kit

Test material for RFC-0001 v3.5 (`docs/A2-DLC_v3.5_protocol.md`), mainly §6.4, the normative receipt. An implementation that
passes the vectors of its class reads and writes the same receipts as the reference binary. The RFC and these files are enough
to write a verifier without reading the Go code.

    python conformance/run.py --binary <verifier or producer> [--engine-image <image>] [--strict] [--reasons a2dlc|none]

The runner needs Python 3.9+, git and ssh-keygen. Docker is needed for the producer vectors that run an engine and for the
engine class. `jsonschema` (with `referencing`) is optional: when present, every receipt, approvals file and engine message is
also validated against `contracts/`; `--require-schema` makes its absence a failure. Exit status 0 means no vector failed.
`--strict` also fails on a skipped vector; CI uses it.

## Classes

| Class | Implementation under test | Entrypoint the runner calls | Vectors |
|---|---|---|---|
| verifier | Decides whether a receipt is valid for a commit | `<binary> verify --commit HEAD`, exit 0 means valid | `vectors/verifier/` |
| producer | Writes receipts and approvals | `<binary> check --base <sha>`, `<binary> approve <target> --key <k> --principal <p>` | `vectors/producer/` |
| engine | Judges changed files inside a container | `docker run` with `/io`, `/base`, `/cand` as in RFC §4.4 | `vectors/engine/` |

A verifier written from the RFC has its own CLI. Wrap it in a script that accepts `verify --commit HEAD`, runs in the
repository's directory, reads the allowed signers from the `A2DLC_ALLOWED_SIGNERS` file, and exits 0 only when the receipt is
valid. Pass `--reasons none` when its messages differ from the reference binary's: the vector is then judged on valid or
invalid only. With `--reasons a2dlc` the runner also looks for the problem kind (RFC §6.4.8) in the output.

## Vector files

Each file under `vectors/<class>/` is `{"class", "about", "template"?, "vectors": [...]}`. A vector has an `id`, a `title`, the RFC
section it exercises (`rfc`) and `expect`. The id prefixes are `V-tree`, `V-receipt`, `V-gates`, `V-note`, `V-approval`,
`V-waiversha` (verifier), `P-check`, `P-approve` (producer) and `E-protocol` (engine).

`repo` describes the repository to build: `{"object_format": "sha1"|"sha256", "commits": [{"files": {path: spec}}]}`. A spec is a
string (a regular file), `{"text"|"b64", "mode"?}`, `{"symlink": target}` or `{"gitlink": oid}`; `null` deletes the path. Each
commit is made from the previous one, so a vector can say "the base had X, the head changes Y". The runner builds commits with
git plumbing, so paths that differ only in case and gitlinks work on any file system. The base commit is the first one, and
`$BASE` in a receipt stands for its id.

Verifier vectors write `.a2dlc/verdict.json` in the working tree from the group `template` plus `receipt.set`/`receipt.unset`
(`receipt: null` writes none; a string is written as it is). The `tree_hash` in a vector is the expected value computed from
the RFC, so a verifier that hashes differently calls the receipt stale. `allowed_signers` holds the allowed-signers file for the
vector. Waivers and approvals are committed in the head commit, as RFC §6.4.7 requires.

`expect` of a verifier vector is `{"valid": bool, "kinds"?: [...], "kinds_any"?: [...], "why"?: [...]}`: `kinds` are the problem
kinds that must appear, `why` the reasons of an `unsigned_waiver`.

Producer `check` vectors give the expected exit status and the fields of the receipt the producer must write (`verdict`,
`tree_hash`, `base_ref`, `targets`, `out_of_scope`, `pending_waivers`, `engine_errors`); `verify` says what the verifier must
say about that receipt. Producer `approve` vectors give the `waiver_sha256` and `file_blob` the approval must hold, and the
runner checks the signature over the text of RFC §6.4.7 with `ssh-keygen -Y verify`.

Engine vectors run the image on a request built from `base_files`, `candidate_files` and `changed_files`, check the response
against `contracts/engine-response.schema.json`, and check that every changed file is accounted for with a target or an
`out_of_scope` entry and that no other file is. They are protocol-level: `--engine-semantic` also compares the verdicts in
`expect.verdicts`, which only makes sense for an engine of the vectors' language.

## Where the values come from

- Tree hashes, waiver hashes and the approval text are computed by `e2e/rfc_reference.py`, written from the RFC text alone, and
  the same file is checked against git and against the frozen output of the Go code in `e2e/test_rfc_reference.py`.
- `V-waiversha-*` and `P-approve-*` come from `e2e/testdata/rfc/waiver-sha-go.json` (what the Go code computes for each
  `waivers.json`). Waivers whose target path holds `< > : " | ? *` or a control character are left out, because git on Windows
  cannot hold such a path; the escape rules for those characters are still covered through the `reason` field.
- `V-gates-*` mirror the nine frozen cases of `e2e/testdata/gates-py/casos.json` as receipts (shape and verdict only: the
  Python gates themselves are not run). `P-check-*` with `derived_from` use the logic of the frozen mutants in
  `e2e/testdata/mutantes-bb/`. Both fixtures are pinned by sha256 and never edited; the runner fails if one changed.
- Signatures are made with `keys/test_ed25519` (allowed) and `keys/stranger_ed25519` (not allowed). They are test keys with no
  passphrase and sign nothing real. ed25519 signatures are deterministic, so the vectors are reproducible.

`python e2e/make_conformance_vectors.py` rewrites the vectors; `--check` fails if the committed ones differ. The generator is
not part of the kit.

## Known failures of the reference

A vector with `known_failure` is expected to fail against the reference binary, and the runner reports it as `XFAIL`. It fails
when the failure disappears, so the marker cannot rot.

- `V-receipt-007`: a receipt with a required field missing is accepted. `DecodeStrict` rejects unknown fields but does not
  enforce `required`, `enum` or `pattern` (bead a2dlc-5fq). RFC §6.4.11 lists this and the other places where the reference is
  more lenient than the schemas.

## What the kit does not cover

- A verifier is judged on `verify --commit HEAD` in a repository with no remote. The `base_unpushed` problem and the pre-push hook
  are not tested.
- Engine vectors check the protocol, not the quality of a language engine.
- Nested untracked repositories in the working tree enter the reference's local preview hash (bead a2dlc-3hd); the vectors
  hash commits only.
- Not every document Go accepts is portable: invalid UTF-8, keys in another case and `null` fields in `waivers.json` are outside
  the schema and outside the vectors (RFC §6.4.11).
