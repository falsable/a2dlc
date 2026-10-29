# Contributing

Read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) first. [GOVERNANCE.md](GOVERNANCE.md) says who decides what. Security problems do not go in issues or pull requests: see [SECURITY.md](SECURITY.md).

## What helps most

- **A bug report with a frozen mutant.** A function pair where `a2dlc check` returns `PASS` and a behavior difference exists, or the reverse. Examples of the format are in [`e2e/testdata/mutantes-bb/`](e2e/testdata/mutantes-bb).
- **An engine for a new language.** An engine implements the request and response contracts in [`contracts/`](contracts) (`engine-request.schema.json`, `engine-response.schema.json`). Open an issue before starting, so the scope is agreed.
- **A change to the protocol or to a contract.** This goes through an RFC first: [rfcs/README.md](rfcs/README.md).

Issues and pull requests are the channel. GitHub Discussions are not enabled on this repository.

## Rules the repository enforces

These are in [CLAUDE.md](CLAUDE.md) and apply to every change:

- **The contract governs.** A change of shape goes first in `contracts/*.schema.json`, with a valid and an invalid example in `contracts/examples/`, then in `internal/contract`. `TestExamplesOfContractDecode` fails when the Go struct and the schema drift apart.
- **The core knows no languages.** Language-specific logic belongs in an engine, never in `internal/`.
- **Frozen files are not edited or regenerated:** the mutants in `e2e/testdata/mutantes-bb/` (pinned by sha256) and the cases in `e2e/testdata/gates-py/`.
- **The gate is the push, never the commit.** `a2dlc install` writes no pre-commit hook.
- `internal/run` imports `internal/app`, never the reverse.

Code, comments, commit messages and Markdown are written in English.

## Run the checks

Go 1.23 is the toolchain. Everything runs in a container, so nothing is installed on your machine:

```sh
docker run --rm --network none -v "$PWD:/src" -w /src -v a2dlc-gocache:/root/.cache \
  -e GOFLAGS=-buildvcs=false golang:1.23-bookworm sh -c 'git config --global --add safe.directory "*"; \
  gofmt -l .; go vet ./... && go test -count=1 ./...'
```

`gofmt -l .` must print nothing. On Git Bash for Windows, prefix the command with `MSYS_NO_PATHCONV=1` and write the mount as `-v "$(cygpath -w "$PWD"):/src"`.

When you change a schema:

```sh
docker build -t a2dlc-contract-test contracts/tests
docker run --rm -v "$PWD/contracts:/c" a2dlc-contract-test python /c/tests/generate_invalid.py /c
docker run --rm -v "$PWD/contracts:/c:ro" a2dlc-contract-test python /c/tests/validate.py /c
```

`validate.py` must end with `0 failures`. Review the diff after `generate_invalid.py`: only the examples you meant to change should differ.

The Python engine has its own unit tests, and the gate measurements need the images described in `CLAUDE.md`. Run the ones that cover what you touched.

The repository gates itself with `a2dlc check` (see `.a2dlc/config.json`). It needs the black-box engine image built from `engines/blackbox/*Dockerfile`. If you cannot build it, say so in the pull request.

## Commits and pull requests

- One topic per pull request. Do not mix a refactor with a behavior change.
- Commit messages follow `type(scope): subject`, for example `fix(engine-py): CrossHair errors on either side are INCONCLUSIVE, not FAIL`.
- The pull request description says what changed and why, and lists the commands you ran with their result. If something was not run, it says so.
- A change to the protocol text or to a schema links the RFC that accepted it.

## License of contributions

The project is Apache-2.0. A contribution you submit is licensed under the same terms, as section 5 of the [LICENSE](LICENSE) provides. This repository has no CLA and no DCO sign-off check.
