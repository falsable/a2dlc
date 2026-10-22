# a2dlc

A2-DLC Lite differential gate, built to serve any language and any agent: a Go core and one engine per language in Docker. Measurements in §12 of protocol v3.4 (`docs/A2-DLC_v3.4_protocol.md`).

Task tracking: beads. Run `bd prime` to see the workflow.

## Commands
Go is not installed on the host, and it will not be installed. Everything runs in `golang:1.23-bookworm`:

    MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$PWD"):/src" -w /src -v a2dlc-gocache:/root/.cache \
      -e GOFLAGS=-buildvcs=false golang:1.23-bookworm sh -c 'git config --global --add safe.directory "*"; \
      gofmt -l .; go vet ./... && go test -count=1 ./... && GOOS=windows GOARCH=amd64 go build -o dist/a2dlc.exe ./cmd/a2dlc'

- E2E of the `.exe` on Windows: `bash e2e/windows.sh <empty temporary folder>`. It uses the fake engine (`engines/fake`) and a bare remote, so the pre-push runs a real `git push`. At the end it validates every verdict the binary writes against the schema.
- The repo gates itself (`.a2dlc/config.json`): `a2dlc check` needs the image `a2dlc-engine-bb-go:0.1` built locally (commands in `engines/blackbox/*Dockerfile`). The mutants in `e2e/testdata/mutantes-bb/` are frozen by sha256: they are not edited or regenerated.
- Test gates (NOP/Oracle, baseline, flakies): `bash e2e/measure_gates.sh <empty temporary folder>` with real pytest over the frozen cases in `e2e/testdata/gates-py/` (not edited or regenerated; Spanish keys, normalized by `e2e/normalize_manifest.py`). Needs `a2dlc-engine-py:0.2` (`engines/py`) and `a2dlc-tests-pytest:0.2` (`e2e/gates/pytest.Dockerfile`, with coverage); `FIXTURE=gates-prop` needs `a2dlc-tests-hypothesis:0.1`. The judge's own tests: `python -m unittest discover -s e2e -p "test_*.py"`. What pytest cannot make deterministic (flaky) is in `internal/app/gates_test.go` with a fake runner. `bash e2e/measure_self_verification.sh <that same folder>` afterwards runs the ADLC "session verifies its own work" rule over the trees measure_gates built (docs/VS_SDLC_AND_ADLC.md).
- Current images built from this tree: `a2dlc-engine-py:0.7` (`engines/py`, 219 unit tests: `docker run --rm --network none -v "<repo>/engines/py:/engine:ro" --entrypoint python a2dlc-engine-py:0.7 -m unittest discover -s /engine -p "test_*.py"`), `a2dlc-roles:0.10` (`roles/`), `a2dlc-egress-proxy:dev` (`roles/egress-proxy`, needed by any role with `"network": "provider"`). Older tags (`engine-py` 0.2–0.6, `roles` 0.1–0.9) are kept for the measurements that cite them.
- Contract test: it is in the README. If you change a schema, run `python contracts/tests/generate_invalid.py` and then `validate.py`.

## Rules
- **The contract governs.** A change of shape goes first in `contracts/*.schema.json`, with valid and invalid examples; then in `internal/contract`. `TestExamplesOfContractDecode` fails if the struct and the schema drift apart.
- **`internal/run` imports `app`, never the reverse.** The state machine calls the oracle; the gates do not know it exists. The policy of what each role sees (`run/policy.go`) is code, not config: `roles.json` chooses the command and cannot widen mounts.
- **The core knows no languages.** The engine is the one that discovers the targets inside the changed files; no TS or Python logic in `internal/`.
- **Gate outputs and signatures stay out of the `tree_hash`** (`gitx.Excluded`). If they went in, approving would make the verdict stale.
- **`check` without `--base` compares against the upstream.** Against HEAD, after committing, it would not see what was committed, and an unsigned waiver would pass the push (covered by `TestCheckAgainstHEADDoesNotWashCommittedChanges`).
- **The gate is the push, never the commit.** `install` writes no pre-commit: agents commit and merge their worktrees with no human in the loop; `pre-push` (`verify`) is where a behavior change needs a signed waiver.
- **Agent hooks go through `a2dlc hook <agent>`.** In Claude, the Stop hook treats exit 1 as "does not block": wiring `check` directly would let FAILs through.

## Never
- An approval signature that does not bind the file's blob: it would be valid for future changes to the same function.
- Read `allowed_signers` from the repo: the agent would add its own key in the same diff.
- Ask for, store or pass the passphrase of the signing key. ssh-keygen accepts it via stdin; the only thing that protects the signature is that the agent does not know it.
- Skip the gate when the binary is missing: the pre-push uses the absolute path and, if it cannot find it, fails.
