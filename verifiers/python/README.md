# Clean-room verifier in Python

A verifier of the A2-DLC receipt (RFC-0001 v3.5, §6.4.8) written with the Python standard library only. It recomputes the tree
hash of a commit, validates `.a2dlc/verdict.json` against `contracts/`, and checks the SSH approval signatures. It is the
acceptance test of the RFC receipt and of the conformance kit: if an implementation written from the text alone passes the
vectors, the text is enough.

**Clean room.** It was written reading only `docs/A2-DLC_v3.5_protocol.md`, `contracts/`, `conformance/` and the Docker commands
in `CLAUDE.md`. Nothing under `internal/`, `cmd/`, `e2e/`, `engines/`, `roles/` or any Go file was opened, and the `a2dlc` binary
was never run to compare outputs. Where the RFC was wrong or silent the point is in [AMENDMENTS.md](AMENDMENTS.md).

## Use

    python a2dlc_verify.py verify [--commit HEAD] [--contracts DIR]

Run inside the repository. It prints `a2dlc verify: ok` and exits 0 when the receipt is valid for the commit; otherwise it prints
one `kind: message` line per problem on standard error (kinds of RFC §6.4.8) and exits 1. Exit 2 means the verifier itself failed
(no repository, unknown commit, no `contracts/`), which the RFC does not separate from an invalid receipt.

Allowed signers come from `A2DLC_ALLOWED_SIGNERS`, else `A2DLC_ALLOWED_SIGNERS_DATA`, else `~/.config/a2dlc/allowed_signers`.
The schemas are read from `--contracts`, `$A2DLC_CONTRACTS`, or `contracts/` of this checkout.

## Choices

- **Signatures are checked in Python, not with `ssh-keygen`.** RFC §6.4.7 names `ssh-keygen -Y verify` as the reference, but the
  task is standard library only and `python:3.12-slim` has no `ssh-keygen`. The file implements Ed25519 from RFC 8032 and reads
  the SSH signature format of OpenSSH `PROTOCOL.sshsig`. Only `ssh-ed25519` is supported, which is what §6.4.7 requires; any other
  key type is refused. An allowed-signers line with an option other than `namespaces=` (`valid-before`, `cert-authority`, ...)
  is ignored, so this verifier approves less than `ssh-keygen`, never more.
- **The tree hash is rebuilt from the definition.** `git ls-tree -r` lists the commit; the excluded paths are removed in Python,
  the tree objects are encoded and hashed with `hashlib`. `git` is only a reader of objects.
- **The schema validator is a subset written for `contracts/`** (draft 2020-12: `$ref` across files, `allOf`, `oneOf`,
  `if/then/else`, `unevaluatedProperties`, `pattern`, `format: date-time`, ...). It does not read arbitrary schemas.

## Tests

The image needs Python, `git` and, for the conformance runner, `ssh-keygen`:

    docker build -t a2dlc-verifier-test:0.1 verifiers/python

Unit tests (stdlib `unittest`, no network):

    MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$PWD"):/r:ro" -w /r/verifiers/python \
      -e PYTHONDONTWRITEBYTECODE=1 a2dlc-verifier-test:0.1 python -m unittest discover -p "test_*.py"

Conformance kit, verifier class:

    MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$PWD"):/r:ro" -w /r a2dlc-verifier-test:0.1 sh -c '
      printf "#!/bin/sh\nexec python /r/verifiers/python/a2dlc_verify.py \"\$@\"\n" > /tmp/verify && chmod +x /tmp/verify
      python conformance/run.py --class verifier --binary /tmp/verify --reasons none'

Run it with `--reasons a2dlc` as well: this verifier's messages contain the phrases the runner looks for, so the problem kinds are
checked too. Both runs end with `145 ok, 1 bad`. The one bad is `V-receipt-007`: the vector says a receipt with a required field
missing is refused, this verifier refuses it, and the runner reports that the reference's `known_failure` marker "no longer
fails" (AMENDMENTS.md, D).
