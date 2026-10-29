# RFC amendments found by the clean-room Python verifier

The verifier in this directory was written from `docs/A2-DLC_v3.5_protocol.md`, `contracts/` and `conformance/` alone (bead
a2dlc-0e8). Where the text was wrong or silent, the code was not consulted; the point is recorded here. The RFC itself is not
edited by this bead.

Result: **one amendment is needed for the verifier class to pass (A1)**. Read literally, §6.4.6 gives a waiver hash that nine
vectors refuse. The other items filled a gap from outside the RFC, or are ambiguities no vector reaches.

## A. The text contradicts the vectors (needed)

### A1. §6.4.6, table of characters in the WaiverSHA string

**Applied** (bead a2dlc-qhw): the table writes the escapes out, and `contracts/tests/check_rfc.py` fails on the old table
(`<!-- a2dlc:waiver-escapes -->`). The text below is the finding as it was made.

- **Ambiguity.** The right-hand column of the table repeats the left-hand character: `<`, `>`, `&` are "written as" `<`, `>`,
  `&`, and U+2028, U+2029 as the raw characters. The prose after the table says "`<` and `<` in `waivers.json` give the same
  hash". Only the sentence "the output of Go's `encoding/json` ... (`HTMLEscape` on)" says that these characters are escaped.
  A reader who follows the table gets another hash.
- **Blocks.** Taking the table literally, 9 vectors fail: `V-waiversha-ch_lt-0`, `-ch_gt-0`, `-ch_amp-0`, `-u2028-0`, `-u2029-0`,
  `-in_escaped_lt-0`, and `V-approval-001`, `-011`, `-013` (the example reason holds `<1> &`, so the RFC's own example hash
  `b4359b1a...` does not come out).
- **Proposed text.** In the table, write the escapes out: `<` -> `\u003c`, `>` -> `\u003e`, `&` -> `\u0026`, U+2028 -> `\u2028`,
  U+2029 -> `\u2029` (lowercase hexadecimal, backslash then `u` then four digits). In the prose: "so `\u003c` and `<` in
  `waivers.json` give the same hash". Everything else in the table is correct as written.

## B. The RFC points outside itself (filled from outside knowledge)

None of these blocks a verifier that shells out to `git` and `ssh-keygen`, as the reference does. They block one that does the
work itself, as this one does for the signature and the tree encoding.

### B1. §6.4.2, "the entries in git's tree order"

The bytes of a tree object are not given. Proposed: each entry is the octal mode as git writes it (`100644`, `100755`, `120000`,
`160000`, and `40000` for a directory, no leading zero), a space, the name in bytes, a NUL, and the raw object id (20 or 32
bytes). Entries are sorted by name compared as bytes, a directory name compared as if it ended in `/`. Vector `V-tree-021`
exercises the order.

### B2. §6.4.7, "OpenSSH signature ... format in OpenSSH `PROTOCOL.sshsig`"

The layout of the armored blob (magic `SSHSIG`, version 1, public key, namespace, reserved, hash algorithm, signature), the data
that is signed (`SSHSIG` + namespace + reserved + hash name + hash of the message, each as an SSH string) and the hash algorithms
accepted (`sha256`, `sha512`) are in an external document. Proposed: cite it by version and URL as a normative reference, and
say which hashes a verifier must accept (the reference accepts what `ssh-keygen` does; the vectors use `sha512`).

### B3. §6.4.7, "A verifier MUST support `ssh-ed25519`"

The verification equation is not stated. OpenSSH checks the cofactorless equation `[S]B = R + [k]A` and rejects `S` not below
the group order. Proposed: say so, or cite RFC 8032 §5.1.7 and add "and `S` MUST be less than the group order L".

### B4. §6.4.7, the allowed-signers file

"A file in the format of `ssh-keygen -Y verify`" leaves open which of its features a verifier must honour: principal patterns
(`*`, `?`, `!`), the `namespaces=` option, `valid-after`, `valid-before`, `cert-authority`, quoted option values. Proposed: a
minimal grammar a verifier MUST implement (`principals [namespaces="..."] keytype base64 [comment]`, patterns as above) and a rule
that a line with an option the verifier does not implement is ignored, so that it approves less and never more. This verifier
implements exactly that and drops lines with other options.

## C. Ambiguities no vector reaches

### C1. §6.4.8, `base_unpushed`

"the fork point of `HEAD` from `refs/remotes/origin/HEAD`" can be `git merge-base --fork-point` (uses the reflog, often empty)
or plain `git merge-base`. "the upstream of `HEAD`" does not say what happens when `--commit` is not `HEAD`. The kit says it
does not test this problem. Proposed: name the git command, and say the pushed base is computed from `HEAD` whatever commit is
verified. This verifier uses `@{upstream}`, else `git merge-base HEAD refs/remotes/origin/HEAD`, from `HEAD`.

### C2. §6.4.8, kinds for unreadable documents

There is no problem kind for a `waivers.json`, `approvals.json` or `config.json` that exists but is not valid JSON or does not
validate. Proposed: for `waivers.json` and `approvals.json` state that the document counts as absent, so every waived target is
`unsigned_waiver`; for `config.json` the rule of §6.4.5 already applies (every target is critical). This verifier does that and
puts the parse error in the message.

### C3. §6.4.8, the reasons of `unsigned_waiver`

The RFC lists "no waiver in the commit, `waiver_changed`, `file_changed` or `signature_invalid`". Vectors `V-approval-002` and
`-003` expect the reason `unsigned`: no approval at all names the target. Proposed: add it to the list.

### C4. §6.4.7, `file_blob` of a target that is not in the tree

"the blob at the file part of `target` in the tree of the commit (§6.4.2)": that tree has the excluded paths removed, and the
file may not exist. Proposed: a target whose file is absent from the tree of §6.4.2 has no valid approval (`file_changed`).
This verifier does that. For a symbolic link or a gitlink the id is the one in the tree entry.

### C5. §6.4.5, `**/` in the middle of a glob

"`**/` matches any number of leading directories including none": it is not said whether `src/**/x.py` matches `src/x.py`.
Proposed: say that `**/` has this meaning wherever it appears in a half. This verifier does that.

### C6. §6.4.7, the class of the decision

§6.4.6 says a waiver covers only targets of its class, but the four conditions of validity in §6.4.7 do not mention the class.
Proposed: say whether a verifier checks it. This verifier follows the four conditions and does not check the class.

### C7. §6.4.3, what "validate against the schema" asserts

The schema declares draft 2020-12, where `format` is an annotation by default, and uses patterns written for ECMAScript (the
end anchor `$` does not match before a final newline in ECMAScript; in Python's `re` it does). Proposed: say that `format:
date-time` is asserted, that patterns are ECMAScript, and that a receipt is one UTF-8 JSON document with no byte order mark,
no `NaN`, no content after the value; and what a verifier does with a repeated key in `verdict.json` (§6.4.6 says only that a
producer MUST NOT write one in `waivers.json`). This verifier asserts `date-time`, anchors patterns at the very end, and refuses
`NaN`, a BOM and trailing content; for a repeated key it keeps the last, as the reference does for `waivers.json`.

### C8. §6.4.8, where the working tree is

"reads `.a2dlc/verdict.json` from the working tree": a verifier run from a subdirectory could read it from there. Proposed: say
the root of the working tree. This verifier uses `git rev-parse --show-toplevel`.

### C9. §6.4.7, allowed signers named but unreadable

The order is "the file named by `A2DLC_ALLOWED_SIGNERS`; the contents of `A2DLC_ALLOWED_SIGNERS_DATA`; `~/.config/...`". If the
first variable is set and the file cannot be read, falling through to the next source could approve with another key list.
Proposed: say that a variable that is set decides, and an unreadable file means no signers. This verifier does that.

## D. Notes on the kit, not on the RFC

- **Applied** (bead a2dlc-mdv, `run.py --implementation`; this verifier now gets `146 ok, 0 bad`). **`known_failure` is a property of the reference binary, but the runner applied it to every implementation.** A verifier that
  refuses `V-receipt-007`, as the vector says, makes the runner print `BAD ... the known failure a2dlc-5fq no longer fails:
  remove known_failure from the vector`, and exit 1. Against this verifier the runner ends with `145 ok, 1 bad`, and the one bad
  is that line. Proposed: a runner option (`--implementation reference`, or `--ignore-known-failures`) so that only the
  reference expects the failure.
- **`--reasons a2dlc` reads the reference's wording.** The patterns in `conformance/run.py` (`REASONS`, `WHY`) are phrases of the
  binary's messages, not of the RFC. This verifier's messages contain them so that the kinds are checked too; an independent
  implementation that does not copy them has to use `--reasons none` and is judged on valid or invalid only.
