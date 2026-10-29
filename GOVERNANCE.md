# Governance

## Who decides today

One person: the maintainer, Aldemar Hernández ([falsable](https://github.com/falsable) on GitHub, copyright holder in [LICENSE](LICENSE) and [NOTICE](NOTICE)). There is no committee, no foundation and no vendor-neutral host. The maintainer decides what is merged, what is released, what the RFC says, what the schemas in [`contracts/`](contracts) contain and how the name is used.

The consequence for an adopter: the project has a bus factor of one, and a review of an RFC or schema change by the maintainer is a review of their own work. The only check on those changes is that they happen in public, with a comment period (below).

The license is Apache-2.0. Anyone may fork, and the license does not depend on this governance staying as it is.

The maintainer confirmed on 2026-10-29 the rules in this file, in [rfcs/README.md](rfcs/README.md) (14-day comment period), in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) (optional fields in `0.x` minor releases without a `schema_version` bump) and in [docs/NAME_AND_CONFORMANCE.md](docs/NAME_AND_CONFORMANCE.md) (forks that keep the verdict semantics may keep the name).

## What is governed

- The protocol: [RFC-0001](rfcs/0001-a2dlc-protocol.md) and later RFCs under [`rfcs/`](rfcs).
- The contracts: `contracts/*.schema.json` and their examples ([docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)).
- The reference implementation: the Go binary and the engines in this repository.
- The name and conformance claims ([docs/NAME_AND_CONFORMANCE.md](docs/NAME_AND_CONFORMANCE.md)).

## How decisions are made

- **Code and docs**: a pull request, merged by the maintainer when it passes the checks in [CONTRIBUTING.md](CONTRIBUTING.md).
- **Anything that changes protocol behavior or the shape of a contract**: the RFC process in [rfcs/README.md](rfcs/README.md). The maintainer accepts or rejects in writing, on the pull request, with the reason.
- **Disagreement**: the maintainer decides and writes the reason down. A contributor who disagrees can fork under the license.

## Adding a co-maintainer

Nobody has been added yet. The rule for the first and later ones:

1. Candidates are people who have had several non-trivial changes merged (code, contracts, RFC text or an engine) and have reviewed other people's changes. Anyone can propose a candidate, including themselves, by opening an issue titled "Governance: co-maintainer proposal" with links to that work.
2. The maintainer decides. If there is more than one maintainer, the existing ones decide together and need unanimity.
3. When accepted, the change is a pull request that adds the person to the list below, and the repository grants them write access. Both happen in the same step.

Once there are two maintainers, a change to an RFC or to `contracts/` needs approval from a maintainer who is not its author, and a change to this file needs all maintainers.

A maintainer who has not taken part in the project for twelve months can be moved to the emeritus list by the others. The sole maintainer cannot be moved by anyone; if they step down without a successor, the project is unmaintained and the license lets anyone continue it.

### Maintainers

| Name | GitHub | Since |
|---|---|---|
| Aldemar Hernández | [falsable](https://github.com/falsable) | 2026 (project start) |

### Emeritus

None.

## How the RFC changes

Through the process in [rfcs/README.md](rfcs/README.md): a proposal as a pull request, a public comment period, a written decision, and a version bump of the RFC when it is accepted. The relation between the RFC version, the binary version and `schema_version` is in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Changing this file

A pull request, merged by the maintainer; once there are co-maintainers, all of them approve it.

## Other policies

- [CONTRIBUTING.md](CONTRIBUTING.md)
- [SECURITY.md](SECURITY.md)
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
