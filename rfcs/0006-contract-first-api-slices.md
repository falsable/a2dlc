# RFC 0006: Contract-first API slices

- **Status:** Draft
- **Comment period:** 2026-11-05 to 2026-11-19 (14 days, [rfcs/README.md](README.md) step 2); comments on the pull request that opened it
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §5 (Stages 1, 1.5, 3 and 4), §6.1; `contracts/slice-contract.schema.json` (`io_contract`), `contracts/config.schema.json` (new optional `api` section), `contracts/defs.schema.json` (engine enum), `contracts/escalation.schema.json` (new kinds)
- **Changes `schema_version`:** no

## Problem

A slice that exposes an HTTP or RPC API is judged today function by function. Nothing checks the implementation against the API contract it claims to serve.

- `io_contract` exists in `slice-contract` (`input_schema_ref`, `output_schema_ref`, `error_taxonomy`), and Stage 1.5 only checks that the refs resolve to files of the candidate or the base. No stage reads what those files say.
- OpenAPI and protobuf files appear only in Stage 6, as blast-radius edges (`integration.json.discovered`). Those edges are recorded and never decide the verdict.
- The Go and CLI engine is black-box over process I/O. An API served over HTTP is outside every engine.

The result: a Builder can change a status code, drop a response field or return a 500 on an edge input, and `a2dlc check` sees, at best, a changed handler function whose differential knows nothing about the wire format. The Circular Trap (§1) is wider here than for a pure function, because the agent that writes the handler also writes the test client.

## Proposal

The API contract becomes part of the locked contract `S*`, and four existing stages read it. Each step reuses a mechanism the protocol already has: probes, waivers, protected paths, checks and engines.

### 0. What works without this RFC (informative)

A repo can get the static half today through `config.checks` (§5, Checks), which is normative:

```json
{"checks": [
  {"id": "openapi-breaking", "image": "<oasdiff image>", "severity": "blocking",
   "command": "oasdiff breaking /base/api/openapi.yaml /cand/api/openapi.yaml --fail-on ERR --format json > $A2DLC_OUT.raw && a2dlc-adapter < $A2DLC_OUT.raw > $A2DLC_OUT",
   "matches": ["api/**"]},
  {"id": "openapi-lint", "image": "<spectral image>", "severity": "blocking",
   "command": "...", "matches": ["api/**"]}
]}
```

The adapter turns the tool's output into `check-response`. This covers lint and breaking changes in `a2dlc check` with no change to the binary. Everything below needs this RFC.

### 1. `io_contract.api` in `S*` (§6.1)

A new optional field:

```json
"io_contract": {
  "input_schema_ref": "...", "output_schema_ref": "...", "error_taxonomy": ["..."],
  "api": {"format": "openapi" | "protobuf", "ref": "api/openapi.yaml", "operations": ["createTransfer", "getTransfer"]}
}
```

- `ref` is a path in the repo. Its blob id enters `contract_hash`, so editing the API file after Stage 1 makes the contract stale, as any other change to `S*` does.
- `operations` names the operations (OpenAPI `operationId`, protobuf `Service.Method`) the slice owns. An operation outside the list that changes is a `protected_boundary` escalation (CB-04).

### 2. Stage 1: structural divergence of the two specifications

When `S_α` and `S_β` both declare `io_contract.api`, the core asks an engine for the structural difference between the two API files (`operation: "api_diff"`). Each difference (a field one requires and the other does not, a status code, an enum value) becomes a divergence item for the Inquisitor, which turns it into a boundary probe as today. A divergence that survives escalates `spec_divergence` (CB-01, Stage 1), unchanged.

The diff is deterministic. The LLM only writes the concrete request and the two possible responses of each probe, as §5 Stage 1 already requires.

### 3. Stage 1.5: lint and breaking changes against the base

1. The API file of `S*` goes through the engine's `api_lint` operation. An error is a lint error of `S*`, like an unresolved schema ref.
2. When the base has a file at the same path, the engine's `api_breaking` operation compares the two. Each breaking change escalates `api_breaking_change` with target `<ref>::<operationId>`.
3. An intended breaking change is declared with a waiver `enforce_new_behavior` on that target and signed as any other (§6.4.6–7). No new waiver class: `<file>::<symbol>` already covers it.

### 4. Stage 3: conformance by construction

A new optional `config.api` section:

```json
"api": {
  "codegen": {"image": "<image>", "command": "oapi-codegen -config api/codegen.yaml api/openapi.yaml"},
  "generated_paths": ["internal/api/gen/**"],
  "serve": {"command": "go run ./cmd/server", "port": 8080, "ready_path": "/healthz"}
}
```

- Before steps 1 to 3 of Tier 0, the core runs `codegen` in its image on the Builder's `/work`, without network. The generated code is what the API file says, not what the Builder wrote.
- `generated_paths` are protected for the Builder: a Builder change there escalates `protected_boundary` with no retry, as `forbidden_paths` does. Only the codegen step writes them.
- Where the generator emits an interface the handlers must implement (strict servers), a handler that does not match the contract fails step 1, the compiler. That is a Tier 0 finding and spends a round.

The core runs a declared image and command. It knows no generator and no language, so the rule "the core knows no languages" holds.

### 5. Stage 4: the Saboteur gets the contract, and a new engine judges the wire

1. **The Saboteur sees the API file.** It is public, like the signatures it already receives (§3). The visibility table in `run/policy.go` gains the file; it still never sees handler code.
2. **A new engine, `api`,** answers two operations on a server it starts with `config.api.serve` inside the sandbox, `--network none`, loopback only:
   - `api_fuzz`: property-based requests generated from the contract (for OpenAPI, Schemathesis is the reference tool). A response that violates the contract (undeclared status, schema mismatch, 5xx) is `FAIL` with the request as counterexample.
   - `api_diff_behavior` (refactor and brownfield): the same generated request sequence sent to the base server and the candidate server; responses compared under `π_S` (strict by default; `ignore_fields` applies to response fields). A difference is `FAIL` with `old` and `new` responses, exactly as the function differential.
3. Targets are `<ref>::<operationId>`. `search_budget` records the number of requests and the seed, so a `PASS` keeps meaning "no difference within this budget" (§6.4.3).

`api` is added to the engine enum in `defs.schema.json`. Its `search_budget` shape is `{"requests": int, "seed": int, "stateful": bool}`.

### What this does not claim

Conformance to the contract is not semantic correctness. The `api` engine finds wire-level violations and divergences; it does not know that a balance must never go negative. The invariants of `S*` (§6.2) and the Saboteur's properties stay necessary. A receipt with `api` targets says nothing more than §6.4.9 already lets it say.

## Compatibility

- Every new field is optional: `io_contract.api`, `config.api`, the engine operations. A config and an `S*` without them behave exactly as today.
- Per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md), an added optional field and an added enum value (`api` engine, the two escalation kinds) do not bump `schema_version`.
- A binary older than this RFC rejects a config or an `S*` that sets the new fields, because documents are decoded strictly. That is the pairing rule working as intended.
- `docs/COVERAGE_AND_BENCHMARKS.md` says both that Stage 1.5 checks `io_contract` refs and that there is "no `io_contract`" (the §6.1–6.3 row). This RFC's implementation fixes that row.

## Alternatives

- **Only `config.checks` (section 0).** Covers lint and breaking changes but not conformance, the Saboteur or the differential over the wire. It is the first step, not the whole answer.
- **An API DSL of our own, or a vendor language with its own language server.** Locks users into a format with no ecosystem and no training data for the models that write the code. OpenAPI and protobuf are what the generators, linters and fuzzers already read.
- **TypeSpec as the source format.** Good tooling, but it compiles to OpenAPI. Accepting OpenAPI covers TypeSpec users with one step they already run.
- **Code-first frameworks that derive the contract from the code (Encore and similar).** The contract would be written by the same agent as the code, which is the Circular Trap this protocol exists to break.

## Verification

- **Fixture:** `e2e/testdata/api-go/`, a small Go server generated with `oapi-codegen` strict server from a frozen `openapi.yaml`, with frozen mutants pinned by sha256 like `mutantes-bb/`: a changed status code, a dropped required response field, a 500 on an empty string, an undeclared operation, and one equivalent refactor.
- **Expected:** every mutant `FAIL` on its `<ref>::<operationId>` target; the equivalent refactor `PASS`; a breaking change without a signed waiver escalates `api_breaking_change`; a Builder edit under `generated_paths` escalates `protected_boundary`.
- **Contracts:** valid and invalid examples for `io_contract.api` and `config.api`, `generate_invalid.py` and `validate.py` green; `check_rfc.py` green after §6.4 references the new engine.
- **Conformance:** new vectors for an `api` target in a receipt, generated by `e2e/make_conformance_vectors.py`.

## Open questions

- Should `api_fuzz` run stateful sequences (OpenAPI links) by default, or only when `S*` declares them? Stateful runs find more and cost more.
- gRPC needs a fuzzer as mature as Schemathesis is for OpenAPI. Until one is measured, `format: protobuf` may stop at Stages 1, 1.5 and 3.
