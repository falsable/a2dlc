# RFC 0001: Autonomous Agentic Development Lifecycle (A2-DLC)

- **Status:** Consolidated draft, version v3.5. §6.4 (the receipt) is normative; the rest of the text is pending empirical validation (see §12).
- **Author:** Aldemar Hernández
- **Text:** [docs/A2-DLC_v3.5_protocol.md](../docs/A2-DLC_v3.5_protocol.md)
- **Version history:** the table at the end of that document lists v3.0 to v3.5 and what each changed.
- **Schemas:** [`contracts/`](../contracts), `schema_version` 1.
- **Reference implementation:** the Go binary in this repository (release tags `v0.x`).

The text of RFC-0001 is not duplicated here. This page records the status and where the text lives, so the text can be revised without moving this page.

## What "draft" means here

- The measured claims are those of §12 of the RFC text. Everything else in it (thresholds, times per tier, budgets) is not validated, as §10 states.
- No implementation other than the reference one exists, and there is no conformance suite yet ([docs/NAME_AND_CONFORMANCE.md](../docs/NAME_AND_CONFORMANCE.md)).
- Changes to the text follow the process in [README.md](README.md). A change to the shape of a document is classified by [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md), which says when `schema_version` bumps.
