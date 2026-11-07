# Goldfish Review Report: Stage 9, Implementation Readiness

**Target:** `docs/designs/ticket-to-push-outside-agent.md` (lines 275-453, `## Detailed Implementation Plan` to the end)
**Preset:** Implementation plan only. Rounds 1 and 2. The findings and line numbers below are round 1's, read on the text before any edit; how both rounds were resolved is at the end.
**Standards:** `implementation-readiness-standard` for `egm-implementation-readiness` and `egm-codebase-details`; none for `egm-style`.
**Context passed to every reviewer:** `CLAUDE.md`, `docs/CLAIMS.md`, `contracts/roles.schema.json`, `contracts/intent.schema.json`, `contracts/escalation.schema.json`, `internal/mcp/server.go`, `internal/run/launcher.go`, `internal/app/install.go`, `internal/run/machine.go`, `skills/a2dlc/SKILL.md`, `.github/workflows/release.yml`. `concepts_brief.md` was withheld on purpose.

## Reviewer Status

| Reviewer | Status | Raw findings |
| --- | --- | --- |
| `egm-implementation-readiness` | completed | 3 Blocker, 12 Major, 3 Nit |
| `egm-codebase-details` | completed | 4 Blocker, 12 Major, 3 Nit |
| `egm-style` | completed | 0 Blocker, 11 Major, 9 Nit |

After deduplication: **6 Blockers, 16 Majors, 8 Nits.** Severity is the highest any reviewer gave. "Checked by the Elephant" means the main session re-ran the evidence with a read-only command after the reviews came back. Everything else is the reviewers' reading and has not been executed.

## Verdict

Not ready. The Technical Plan holds, but the file list is not something an implementer can follow without making decisions. Most of the six Blockers come from one cause: the plan says what changes, not how. It names no mechanism for embedding the template, no content for the template, no pull step for the proxy, and no default for `non_goals`. It also misses files whose call sites break when a signature changes.

## Prioritized Findings

### 🔴 Blocker

#### R-1. Removing the MCP filler breaks every call that omits `non_goals`, the existing test included

* **Reviewer Lens:** `egm-codebase-details` (Blocker), `egm-implementation-readiness` (Major M5)
* **Affected Scope:** lines 358-366 (files 15, 16)
* **Discrepancy Description:** The plan deletes the three filler assignments, including the `non_goals` one at `server.go:463`, and calls `Validate()` in `writeIntent`. `Validate()` rejects a nil `non_goals` (`internal/contract/intent.go:136-148`). `stringListArg` returns nil when the argument is absent. The test at `server_test.go:99` passes `invariants` and `acceptance` but not `non_goals`, so it would fail. The Technical Plan claims it keeps passing. Separately, `dispatchTool` (`server.go:311`) never enforces `InputSchema.required`. Adding names to `required` only documents them; the refusal would come from `Validate()`'s generic "is missing" text.
* **Rationale & Recommendation:** State that `non_goals` falls back to `[]` (as `critical_paths` does at `server.go:471`). Add an explicit check in `toolIntentFromTicket` that returns a named error for missing `invariants` or `acceptance`. Add a test for a call that omits `non_goals`, and for a bad `mode` or `criticality` (Goal 2). *Checked by the Elephant:* the nil check in `Validate`.

#### R-2. `install` cannot `go:embed` a file under `contracts/`

* **Reviewer Lens:** all three
* **Affected Scope:** lines 296-299, 306-309 (files 4, 6)
* **Discrepancy Description:** `//go:embed` cannot reach a parent directory, so `internal/app` cannot embed `contracts/examples/valid/roles__install-template.json`. The repo has no `go:embed` and no `.go` file under `contracts/`. Line 309 also leaves the image placeholder to "the implementer". The schema pattern `^[A-Za-z0-9][A-Za-z0-9._/:@-]*$` accepts a token such as `a2dlc-roles:TEMPLATE` and rejects `${...}`.
* **Rationale & Recommendation:** Name the mechanism and list its file. Option one is a new `contracts/embed.go` package. Option two is a copy under `internal/app/` with a test asserting byte equality with the example. Fix the placeholder token. *Checked by the Elephant:* no `go:embed` in the repo.

#### R-3. On a fresh machine the egress proxy is never pulled, so CUJ 1 fails

* **Reviewer Lens:** `egm-implementation-readiness` (Blocker B1), `egm-codebase-details` (Major M6)
* **Affected Scope:** lines 291-294, 321-324, 419-426
* **Discrepancy Description:** `resolveProxyImage` (`launcher.go:258-265`) runs only `docker image inspect` and fails when the image is not local. Nothing pulls it. The plan changes only the error text to "pull it". CUJ 1 says every image is pulled by digest with no manual step. Related: the role image's digest is read before `docker run` pulls it (`launcher.go:188`), so on a first launch `image_digest` is null in the ledger.
* **Rationale & Recommendation:** Decide whether `resolveProxyImage` pulls a registry reference when `inspect` fails, with a timeout and an error text. The alternative is a manual `docker pull` in the skill, `install --roles` output and `doctor`. Say whether the null first-launch digest is accepted or fixed. *Checked by the Elephant:* no `pull` call in `launcher.go`.

#### R-4. The template's content is not specified, and part of it is security-relevant

* **Reviewer Lens:** `egm-implementation-readiness` (Blocker B3), `egm-codebase-details` (Major M10)
* **Affected Scope:** lines 299, 306-309
* **Discrepancy Description:** The plan gives six role names, the network mode and `env_file`. It does not give these:
  * the family of each role (`checkFamilies` forces α and β apart, so two vendors are needed);
  * `argv`, which the schema requires (`roles.schema.json:35`);
  * `env` (`A2DLC_LLM`, `A2DLC_MODEL`);
  * `egress_allow`;
  * timeouts;
  * the variable names one shared credential file must hold.
  
  `egressAllowlist` (`launcher.go:323-331`) refuses a provider role without `egress_allow` unless `A2DLC_LLM` is `openrouter` or `entropos`. The claude and gemini hosts are "not measured" (`launcher.go:77-79`).
* **Rationale & Recommendation:** Put the full template JSON in the plan, including the backend choice. Openrouter for every role is the only measured path. *Checked by the Elephant:* `egressAllowlist` and the `argv` requirement.

#### R-5. A verification step cannot pass: `a2dlc version` prints no image references

* **Reviewer Lens:** all three
* **Affected Scope:** line 425
* **Discrepancy Description:** `cmd/a2dlc/main.go:82-84` prints only the version string. No file in the plan changes it.
* **Rationale & Recommendation:** Add a `version` output change as a listed file, or verify the baked digests another way: the `config.json` that `install` writes in a scratch repo, or `doctor`. *Checked by the Elephant.*

#### R-6. Changing `Install`'s signature breaks ten call sites in files the plan does not list, and its test file does not exist

* **Reviewer Lens:** `egm-codebase-details` (Blocker B4), `egm-implementation-readiness` (Major M4)
* **Affected Scope:** lines 296-304 (files 4, 5)
* **Discrepancy Description:** `internal/app/install_test.go` does not exist. The `Install` tests are in `internal/app/app_test.go` (seven calls) and `internal/app/tests_hint_test.go` (three calls), and none of those files is listed. File 5's "passes the same validation `run` applies" cannot be written inside package `app`, because `run` imports `app`. The validation lives in `run` (`validateRoleConfig` in `cb02b.go:120`, `Machine.readRoles`). The phrase "the mechanical choice is the implementer's" leaves the signature open.
* **Rationale & Recommendation:** Fix the signature. List the call-site edits in both test files. Make file 5 a Create or retarget it. Put the validation test in `internal/run`, or in an external test package. Give the exact report strings. *Checked by the Elephant:* the file's absence and both call-site files.

### 🟡 Major

#### R-7. The new `app` variables need exported accessors, and `EgressProxyImage` is used by tests the plan does not list

* **Reviewer Lens:** `egm-codebase-details` M5, `egm-implementation-readiness` M2
* **Affected Scope:** lines 291-299, 316-324 (files 3, 4, 8, 9)
* **Discrepancy Description:** `enginePyImage` is unexported. `cmd/a2dlc`, `internal/run` and `doctor` need to read the new variables, but no accessor is named. `EgressProxyImage` is an exported const used in `launcher_test.go` (five sites, including `TestEgressProxyImageTagIsVersioned`) and `run_test.go` (two sites). `run_test.go` is not listed.
* **Rationale & Recommendation:** Name each variable, its accessor and its `-X` flag. Decide whether the exported `EgressProxyImage` name survives. List `run_test.go`.

#### R-8. "In the same loop" does not fit `release.yml`

* **Reviewer Lens:** all three
* **Affected Scope:** lines 291-294 (file 3)
* **Discrepancy Description:** The builds are separate lines (53-56). The push loop (58-61) iterates `a2dlc-engine-$m`, which `a2dlc-roles` and `a2dlc-egress-proxy` do not match. The images step spans 45-62, not 50-61. The plan does not give:
  * the build contexts (`roles/` and `roles/egress-proxy/`);
  * the `--label "$fuente"` source label;
  * the three digest grep patterns.
  
  The roles build runs `npm ci` inside a job with `timeout-minutes: 30`, and the plan does not assess that limit.
* **Rationale & Recommendation:** Rewrite the entry as "three builds after line 56, the push and inspect loop over full image names". Add the grep patterns and an estimate of the job time.

#### R-9. `plan run` has its own `--z3-image` default, which the plan misses

* **Reviewer Lens:** `egm-implementation-readiness` M3
* **Affected Scope:** lines 316-319 (file 8)
* **Discrepancy Description:** `cmd/a2dlc/plan.go:55` repeats the literal `a2dlc-engine-z3:0.1`, and the usage text at `main.go:55` hard-codes it too. After the change, `plan run` would still default to an unpublished local tag.
* **Rationale & Recommendation:** List `cmd/a2dlc/plan.go` and the usage-text change. *Checked by the Elephant.*

#### R-10. The `env_file` expansion and the "inside the repository" refusal have no carrier and no algorithm

* **Reviewer Lens:** all three
* **Affected Scope:** lines 321-324, 331-334 (files 9, 10)
* **Discrepancy Description:** `LaunchSpec` and `DockerLauncher{Bin}` carry no repository directory. `LaunchSpec` is built at three sites (`internal/run/invoke.go:127`, `cmd/a2dlc/propose.go:171`, `internal/plan/planner.go:147`). `DockerLauncher` is built at five (`cmd/a2dlc/main.go:206,315`, `cmd/a2dlc/plan.go:234,300`, `internal/run/machine.go:147`). None of these files is listed. The plan leaves these open:
  * the expansion grammar (`${NAME}` only or `$NAME` too, whole value or substring);
  * the containment test (symlinks, relative paths, Windows case and drive letters);
  * which directory counts for `plan run` worktrees;
  * the error texts.
  
  File 9 also bundles two changes, and the plan's own rule puts 9a before 9.
* **Rationale & Recommendation:** Choose the carrier and list every construction site. Specify the grammar and the containment check (absolute path, `EvalSymlinks`, prefix test). Split file 9 in two and order 9a first. Reuse the existing helpers in `launcher_test.go` (the malformed `env_file` and reserved-variable tests).

#### R-11. The "role absent" hint has no mechanism, and the stage files that would carry it are excluded

* **Reviewer Lens:** `egm-codebase-details` M8, `egm-implementation-readiness` M9, `egm-style` F6
* **Affected Scope:** lines 336-344 (files 11, 12)
* **Discrepancy Description:** There is no single "stage skipped, role absent" reason. Each stage writes its own:
  * Stage 1: `reasonWithoutDualSpec`, matched by prefix in `skipStale` (`machine.go:132`);
  * Stage 3: `stage3.go:149`;
  * Stage 4: `saboteur.go:359`;
  * Stage 5: `stage5.go:112`.
  
  In CUJ 1 (Lite) the visible reasons come from `stage1.go` and `stage3.go`. The plan lists `stage1.go` as deliberately untouched.
* **Rationale & Recommendation:** Name the `Machine` field, the stages that append the hint, the exact sentence, and that the text is appended so the prefix match keeps working. List the stage files and the tests that assert these reasons.

#### R-12. Fourteen generated invalid examples and one e2e script also carry the host path

* **Reviewer Lens:** `egm-codebase-details` M9, `egm-implementation-readiness` M6
* **Affected Scope:** lines 311-314, 422 (and the security paragraph above the range)
* **Discrepancy Description:** `contracts/tests/generate_invalid.py` builds 14 `contracts/examples/invalid/roles__*.json` files from the three valid examples. All 14 carry the host path, and so does `e2e/measure_ts.sh`. The plan edits only the three valid files and says the generator "is not needed".
* **Rationale & Recommendation:** Add "run `generate_invalid.py`" and list the 14 files. Decide what to do with `e2e/measure_ts.sh`. Correct "all three" and line 422. *Checked by the Elephant:* 14 invalid files and 1 script.

#### R-13. The install scripts still install v0.5.0, so an outside agent gets neither `--roles` nor the new skill

* **Reviewer Lens:** `egm-codebase-details` M11, `egm-implementation-readiness` M7, `egm-style` F9
* **Affected Scope:** lines 346-349 (file 13), Verification Plan
* **Discrepancy Description:** `install.sh:8`, `install.ps1:7` and `templates/ci/a2dlc.yml:39` pin v0.5.0. Fetching the skill "at the `VERSION` tag" therefore gets v0.5.0's skill. The bump can only follow the release, so the order is circular. `install.ps1:49` has no fallback and fails the whole install if the file is missing at the tag. `install.sh:78` fails silently. No check covers files 13 and 3 beyond the manual cold install.
* **Rationale & Recommendation:** Add a post-release step that bumps the three pins, or have the scripts resolve the latest release. Give the URL form and the fallback. Add a check that fetches the skill from the tag and compares it with the tree.

#### R-14. Anonymous access: wrong scope, wrong release, and an outcome table with two rows for four outcomes

* **Reviewer Lens:** `egm-style` F1, `egm-codebase-details` M12, `egm-implementation-readiness` M10
* **Affected Scope:** lines 281-289, 425 (files 1, 2)
* **Discrepancy Description:** The plan measures one existing package. The three new packages are pushed for the first time by file 3, and ghcr visibility is per package. The plan does not say which release the measurement targets: v0.5.0 before the change, or the new one after it. The result can be mixed (asset public and image private, or the reverse), but file 2 offers only "delete or rewrite". `templates/ci/a2dlc.yml:44-53` still downloads with `secrets['A2DLC_DOWNLOAD']` and logs in to ghcr, so a comments-only edit leaves the live steps wrong. Changing visibility is a GitHub settings action, and the plan does not say who has the access.
* **Rationale & Recommendation:** Measure every baked digest after the new release. Add a "set visibility on the new packages" step with its owner. Give a four-row decision table, and say which lines of the template's steps change in each row.

#### R-15. `doctor` is under-specified, and its git check is unreachable

* **Reviewer Lens:** all three
* **Affected Scope:** lines 385-410 (files 21-24)
* **Discrepancy Description:** `main.go:87-94` opens the repository before the command switch and returns 64 outside one. The "git repository present" check can therefore never report `ok: false`, and the 64 collides with the exit code reserved for bad flags. The "two exported functions taking a repo directory and a docker binary" do not match the code. `readRoles` is a `Machine` method that reads `m.app.Repo.Dir`. `resolveProxyImage(ctx, bin)` returns three values and takes no repository. The credential check needs `readEnvKeys`, which is unexported and not in the export list. The proxy check duplicates the image check. The whole stage is provisional on file 20, but the entries do not say so.
* **Rationale & Recommendation:** Dispatch `doctor` before `gitx.Open`, as `version` is. Give the signatures. Reuse `engine.DockerTests.ImageDigest` (`engine.go:273`). Freeze a first-cut check list, or mark files 21-23 "provisional until file 20" with a decision rule.

#### R-16. "Validate `from_slice` with the slice regex": the regex is unexported in another package

* **Reviewer Lens:** `egm-codebase-details` M15, `egm-implementation-readiness` M5
* **Affected Scope:** lines 360-361 (file 15)
* **Discrepancy Description:** `sliceRe` is unexported in `internal/contract/intent.go:107`, and so is `run.sliceValid` (`machine.go:401`). The plan names no exported helper, and `intent.go` is not listed. The named constant for the signal tool's filler is not named either.
* **Rationale & Recommendation:** Add an exported `contract.ValidSliceID` (or similar) and list `intent.go`. Name the constant.

#### R-17. The `docs/CLAIMS.md` entry leaves its decisions open

* **Reviewer Lens:** all three
* **Affected Scope:** lines 370-374 (file 17)
* **Discrepancy Description:** The plural `escalations.schema.json` is planned work: `CLAIMS.md:42` lists it with the exit-code table under bead `a2dlc-pe6.4.2`, and the design's Future Improvements agrees. It is not a typo, yet the entry still says "if the plural is a typo". Only the measurement column of P2 is rewritten, not the claim column. The `grep` and `jq` commands, the change-class check and the cost source are referred to but not written; `escalations.json` has the shape `items[].kind`. The phrase "the implementer confirms" names no person and no outcome. The file is untracked, so `a2dlc check` over the diff will not see it.
* **Rationale & Recommendation:** Mark line 18 as planned. Rewrite the claim text as well. Write the exact commands. Make tracking the file a decision for the author before implementation starts.

#### R-18. `TestExamplesOfContractDecode` is a struct decode, not schema validation

* **Reviewer Lens:** `egm-codebase-details` M10
* **Affected Scope:** lines 306-309 (file 6)
* **Discrepancy Description:** `contract_test.go:146-167` decodes strictly into Go structs. Schema validation is done by `contracts/tests/validate.py` (`release.yml:34`). The template would also have no `builder_orthogonal`, so only the α and β pair constrains families.
* **Rationale & Recommendation:** Name both checks for the template.

#### R-19. "No schema change" contradicts file 9a

* **Reviewer Lens:** `egm-style` F8
* **Affected Scope:** lines 414, 422, 423
* **Discrepancy Description:** File 9a edits the description in `contracts/roles.schema.json`. The Verification Plan says there is no schema change and that no schema is altered. `CLAUDE.md` says to run `generate_invalid.py` and `validate.py` after a schema change, and R-12 needs the generator anyway.
* **Rationale & Recommendation:** Write "no shape change; description text only". Run the generator for R-12 and `validate.py`, and check whether `check_rfc.py` or the conformance vectors hash the schema.

#### R-20. The section's framing does not hold: "listed once", undefined Stage D and E, and stage names that collide with the run's stages

* **Reviewer Lens:** `egm-style` F5, `egm-codebase-details` N17, `egm-implementation-readiness` N2
* **Affected Scope:** line 277 and the group headings
* **Discrepancy Description:** Three files are listed twice (files 1 and 20, 14 and 24, 18 and 24). "Stage D and E" are never defined in the document. "Stage 0 / A / B / C" reuses the names of the run's own stages. Stage 0 holds 14 files with no buildable checkpoint in between.
* **Rationale & Recommendation:** Rename the groups to phases. Split phase 1 into buildable sub-phases (variables, launcher and release with `go test` green; template and `install --roles`; documents and scripts). Drop the Stage D and E sentence, or define the terms.

#### R-21. File 19, the `rfcs/0007` marker, has no consumer

* **Reviewer Lens:** `egm-style` F10 (Major), `egm-codebase-details` N18, `egm-implementation-readiness` N1
* **Affected Scope:** lines 380-383
* **Discrepancy Description:** `check_rfc.py` scans only `docs/A2-DLC_v3.5_protocol.md` (line 14), and its tag attaches to the line before a fenced block or table. RFC-0007 line 6 is a metadata bullet that already says "new", and the RFC is in its comment period. No Goal depends on the marker.
* **Rationale & Recommendation:** The author decided during drafting to keep the marker, so this finding is likely to be contested. If it is kept, give the exact marker text and drop the claim that it follows `check_rfc.py`'s style.

#### R-22. The `SKILL.md` entry names the wrong section

* **Reviewer Lens:** `egm-codebase-details` M16, `egm-implementation-readiness` M11
* **Affected Scope:** lines 376-378 (file 18)
* **Discrepancy Description:** The install paragraph is section A (lines 27-41). Section B is `check`.
* **Rationale & Recommendation:** Say section A.

### 🟢 Nit

* **R-23** (line 284, `egm-style` N2, `egm-codebase-details` N19). Line 695 of `COVERAGE_AND_BENCHMARKS.md` sits under "Role images", not "Installation". Drop "touches the image section anyway" and give the insertion points.
* **R-24** (line 313, `egm-style` N3). "Raise `a2dlc-roles:0.1` to the current tag" is ambiguous: local builds use 0.15, and the release uses its version. Name the tag or the substitution.
* **R-25** (lines 405, 428, `egm-style` N5). "The JSON shape is stable" and "every path they cite exists" are not measurable. Name the fields and the path-exists loop.
* **R-26** (line 354, `egm-style` N6). The README MCP row lacks both intent tools, but the rationale names only one.
* **R-27** (line 425, `egm-style` N7). The release is triggered by pushing a `v*` tag, not by "a tag on a branch".
* **R-28** (line 453, `egm-style` N8). "*Drafted with EGM*" reads as an endorsement next to Alternative B. Drop it, or say how EGM was used.
* **R-29** (lines 435, 441, `egm-style` N9). Both references point at sources a reader cannot open: `concepts_brief.md` is untracked, and the EGM artifacts are outside the repository.
* **R-30** (lines 363-366, `egm-implementation-readiness` N3, `egm-codebase-details` reuse notes). The test entries do not say whether they drive `Serve` or call `dispatchTool` directly. Outside the range, line 271 says doctor runs seven image inspects while file 22 says five.

## Traceability Matrix

| Consolidated | implementation-readiness | codebase-details | style |
| --- | --- | --- | --- |
| R-1 | M5 | B1 | |
| R-2 | B2 | B2 | F3 |
| R-3 | B1 | M6 | |
| R-4 | B3 | M10 (families) | |
| R-5 | M12 | B3 | F2 |
| R-6 | M4 | B4 | N4 |
| R-7 | M2 | M5 | |
| R-8 | M2 (loop) | M13 | F4 |
| R-9 | M3 | | |
| R-10 | M1 | M7 | F6 |
| R-11 | M9 | M8 | F6 (file 11) |
| R-12 | M6 | M9 | |
| R-13 | M7 | M11 | F9 |
| R-14 | M10 | M12 | F1 |
| R-15 | M8 | M14 | F11 |
| R-16 | M5 (from_slice) | M15 | |
| R-17 | M11 | M16 | F7 |
| R-18 | | M10 | |
| R-19 | | | F8 |
| R-20 | N2 | N17 | F5, N1 |
| R-21 | N1 | N18 | F10 |
| R-22 | M11 | M16 | |
| R-23 | N1 (line 695) | N19 | N2 |
| R-24 to R-30 | N3 | reuse notes | N3, N5-N9 |

## Coverage and Limits Reported by the Reviewers

* No reviewer ran code, tests or the release workflow, and none opened a URL.
* These files were not read by any reviewer, or only through grep hits: `roles/adapter.py` and `roles/prompts/`, which matter for R-4's backend and egress hosts; `cmd/a2dlc/main_dispatch_test.go`; `docs/STATE_MACHINE.md`; `contracts/tests/validate.py`; `verifiers/python`; the conformance vectors, which matter for R-19; and most of `docs/COVERAGE_AND_BENCHMARKS.md` and `README.md`.
* Not verifiable from the repository: anonymous readability of the release assets and the ghcr images, whether tag v0.5.0 contains `skills/a2dlc/SKILL.md`, and how long the release job takes with three more images.

## Resolution, 2026-11-07

The author delegated every open decision to the drafting session. The design was revised in place; "TP" is its Technical Plan and file numbers are those of the revised Detailed Implementation Plan. Facts read or measured for the resolution, beyond the reviewers' reading:

* Anonymous access, measured 2026-11-07 from a shell with no GitHub session: `SHA256SUMS` of v0.5.0 answered 200, and the manifests of the four images in that release's `IMAGENES.txt` answered 200 to an anonymous ghcr token.
* The last release job took about 4 minutes (2026-11-03).
* A Lite run launches only `builder_primary` and `saboteur`; the specifiers, the Inquisitor and the back-translator run only in Stages 1 and 1.5.
* No role has run the Claude CLI inside the roles image; every measured run used OpenRouter.
* `env_file` appears only in `internal/run/launcher_test.go` among the Go tests.
* The pin of the install scripts is deliberate: each release is followed by a commit that bumps it in the installers, the CI template and the self-gate.

| Finding | Outcome | Where |
| --- | --- | --- |
| R-1 | Fixed. `non_goals` falls back to `[]`; `invariants` and `acceptance` are checked in code with a named error; `required` is documentation only. | TP §3; files 29, 30 |
| R-2 | Fixed. `contracts/embed.go` embeds the template; the placeholder is `a2dlc-roles:TEMPLATE`, which matches the image pattern. | TP §2; files 9-11 |
| R-3 | Fixed. The launcher pulls a missing registry reference (`ensureImage`) before it reads a digest, for the proxy and the role image, so the first launch records `image_digest`. | TP §1; files 2, 3 |
| R-4 | Fixed. The template is written out in full: the two roles Lite launches, Claude CLI on the operator's subscription, `egress_allow: ["api.anthropic.com"]`. Step S1 measures it before release, with a decision rule whose fallback is OpenRouter. | TP §2; Alternative O |
| R-5 | Fixed. `a2dlc version` prints the version and the five pinned references. | files 4, 6 |
| R-6 | Fixed another way. `Install`'s signature does not change; a new `WriteRolesTemplate` method, and the validation test lives in package `run`. | files 11, 12, 16; Alternative P |
| R-7 | Fixed. Each reference stays in its owning package as a string variable set by `-X`; `EgressProxyImage` keeps its name as a `var`, so its seven test sites compile unchanged. | TP §1, table |
| R-8 | Fixed. Builds, contexts, label, loop over full names, `grep` patterns and the timeout assessment are written out. | file 7 |
| R-9 | Fixed. | files 4, 5 |
| R-10 | Fixed another way. No carrier: `ResolveEnvFile` runs at the three readers of `roles.json`, which know the repository; the grammar and the containment check are specified; the launcher refuses an unresolved value. `plan run` worktrees are a recorded shortcut. | TP §2; files 13-18 |
| R-11 | Fixed another way. One stdout line from `a2dlc run`; the stage reasons do not change. | TP §2; files 19, 20; Alternative Q |
| R-12 | Fixed. The 14 generated files, `e2e/measure_ts.sh` and one example in `docs/COVERAGE_AND_BENCHMARKS.md` are listed; a `git grep` in the Verification Plan. | files 21, 22, 26, 27 |
| R-13 | Fixed. `SKILL.md` ships as a release asset covered by `SHA256SUMS`; the pin bump is Phase 4, as the existing practice does it. | TP §1; file 24; S4 |
| R-14 | Fixed. The measurement for v0.5.0 settled the template's paragraph; S3 checks the seven images after the new release and names the owner action. The four-row table is not needed once the measurement exists. | The Problem item 4; file 23; S3 |
| R-15 | Fixed. `doctor` is dispatched before `gitx.Open`; `LoadRoles` and `EnvKeys` are the exported functions; images go through `ImageDigest`; Phase 5 is marked provisional with a rule for skipping it. | TP §5; files 35-38 |
| R-16 | Fixed. `contract.ValidSliceID`; the constants are named. | files 28, 29 |
| R-17 | Fixed, one point contested. The plural is marked planned; the commands are written; tracking the file is decided (bead `a2dlc-pe6.5`). The claim cell is not rewritten, because RFC-0007 pre-authorization is what P2 claims; the measurement cell gains the scope of the first measurement. | TP §4; file 31 |
| R-18 | Fixed. | Verification Plan, Contract |
| R-19 | Fixed. "Description text only"; the generator and `validate.py` run; `check_rfc.py` and the conformance vectors do not read the roles schema. | file 8; Verification Plan |
| R-20 | Fixed. Five phases with a buildable end each; second touches are marked; the Stage D and E sentence is gone. | Detailed Implementation Plan |
| R-21 | Contested, entry fixed. The author kept the marker during drafting; its consumer is the doc-versus-code rule of TP §6; the claim about `check_rfc.py`'s style is gone. | file 33 |
| R-22 | Fixed. | file 32 |
| R-23 | Fixed. | file 27 |
| R-24 | Fixed: `a2dlc-roles:0.15`. | file 21 |
| R-25 | Fixed: JSON keys and a path-exists loop. | file 38; Verification Plan |
| R-26 | Fixed. | file 25 |
| R-27 | Fixed. | S2 |
| R-28 | Fixed: the footer says how EGM was used. | design footer |
| R-29 | Fixed: the design no longer cites the brainstorm brief; the acceptance-script inputs are inline in TP §6. The brief stays outside the repository. | TP §6 |
| R-30 | Fixed: the new tests call `dispatchTool`; `doctor` runs five inspects. | file 30; NFR |

## Round 2 (re-audit), 2026-11-07

Two fresh reviewers re-read the revised Detailed Implementation Plan against the code: implementation-readiness and codebase-details, with the same reading list as round 1. The round found 0 Blockers. It found 12 distinct Majors after merging the ones both reviewers raised, and one wrong path. The main session checked each one against the code before fixing it. This is the one re-audit the stage allows, so no round 3 runs; what is left open is listed at the end.

| ID | Finding | Resolution | Where |
| :--- | :--- | :--- | :--- |
| R2-1 | `registryRef` as written treats `a2dlc-roles:0.15` as a registry reference, because a reference with no `/` has a `:` before its first `/`, so a local tag would be pulled. | Docker's rule: a `/` must be present and the first segment contains `.` or `:` or is `localhost`. A missing local role image gets its own error text, and file 3 tests that local tags are not pulled. | TP §1; files 2, 3 |
| R2-2 | Once the role image is inspected first, the "image missing" case of `TestEgressProxyThatCannotBeIdentifiedFailsClosed` (`launcher_test.go:423`) stops before it reaches the proxy. | The fake's rule is narrowed to the proxy image. | file 3 |
| R2-3 | `check` hashes the working tree with untracked files and `verify` hashes the pushed commit, so an untracked `roles.json` makes every receipt stale on push. | `roles.json`, `config.json` and `.gitignore` are committed once after install; `install --roles` prints that line; Alternative R records the `.gitignore` option. | TP §2, §4; files 19, 32 |
| R2-4 | The containment test "Rel does not start with `..`" refuses `..name.env` outside the rule's intent and is undefined across Windows volumes. | A pure `outsideRepo` helper: Rel error, `..`, or `..` plus separator; tests for `..name.env` and another volume. | TP §2; file 14 |
| R2-5 | Three tests needed process-level tricks: stdout capture for `version`, `chdir` for `install --roles` (`t.Chdir` needs Go 1.24, the repo builds with 1.23), and a fake `docker` on `PATH` for `doctor`. | `printVersion(w)`, `rolesInstallLines(a)` and `doctor(args, dir, docker, stdout)` take what they need as arguments. | files 4, 6, 19, 20, 35-38 |
| R2-6 | `writeIntent` compared the existing file with the marshalled bytes, but it writes them with a trailing newline, so an unchanged intent never compared equal. | Compare with `append(b, '\n')`. | TP §3 |
| R2-7 | The change-class check matched `protected_paths` with a shell glob and looked for waivers and approvals in the run directory; both files are repo-level. | Check the intent's `critical_paths` and that the pushed diff leaves `.a2dlc/waivers.json` and `.a2dlc/approvals.json` unchanged; the Go matcher under `verify --strict` decides protected paths. | TP §4 |
| R2-8 | Human touches and wall time could not be scored: `state.json`'s timestamps are not wall time, the escalation glob fails when no run escalated, and the terminal condition had no command. In CLAIMS, the "measurement cell" does not exist; the columns are Number published and Reproduce. | A `trial.jsonl` log with its keys, `jq` commands for touches and wall time, a glob-safe escalation count, the `verify` and `ls-remote` commands, and the CLAIMS cells by name. | TP §4; file 31 |
| R2-9 | S1 named no images, repository, intent or pass condition. The proxy is removed after each role, so its log cannot be read afterwards. The owner and the fallback's model and files were missing. | Five numbered steps with commands, an explicit intent, the ledger and `candidate.json` as pass condition, the live `docker logs -f`, the author as owner, and the fallback's model (`anthropic/claude-haiku-4.5`) and files (9, 16, 19, 25, 32). | TP §2; S1 |
| R2-10 | The install scripts would install no skill with the pinned v0.5.0, which ships no `SKILL.md`. That is a regression from today. `install.ps1`'s `try`/`catch` would also swallow a hash mismatch. | Fall back to the `main` URL with a warning; a mismatch exits 1 with the binary kept; the `catch` covers only the download. The mismatch branch has no automated test, and the Verification Plan says so. | file 24; Verification Plan |
| R2-11 | The documents check matched every backticked file name, runtime paths such as `.a2dlc/verdict.json` included. | Only paths under the repository's top-level folders. | Verification Plan |
| R2-12 | `doctor` reported a registry image that is not pulled yet as a failure, which is the state of every fresh machine. It said nothing for an absent `roles.json`, and no release step shipped it. | Not pulled is `ok: true` with a detail; an absent `roles.json` is `ok: false` with the hint; S5 releases it. | TP §5; files 36-38 |
| R2-13 | The quickstart candidate path missed `repo/`. | `examples/quickstart/repo/candidates/pricing_fixed.py`. | TP §6 |

Left open after round 2, by design: S1 needs the author's token, so whether the Claude CLI answers behind the egress proxy is still unmeasured; the install scripts' mismatch branch is untested; and no fresh reviewer has read the round-2 text.
