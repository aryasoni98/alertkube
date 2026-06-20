# alertkube Governance

This document describes how the alertkube project is governed. It is intentionally
lightweight and modeled on the governance of small-to-mid CNCF Sandbox projects.
It will evolve as the contributor base grows.

## Values

The alertkube community holds these values:

- **Openness** — discussion, decisions, and roadmap happen in public (GitHub
  issues, pull requests, and discussions) wherever possible.
- **Neutrality** — alertkube is not controlled by any single company. Decisions
  are made on technical merit, not commercial interest. No vendor receives
  preferential treatment in the codebase, roadmap, or documentation.
- **Determinism** — the project's core promise is predictable, explainable
  alerting. Features that compromise that (e.g. nondeterministic/AI-driven
  routing in the critical path) face a high bar.
- **Respect** — all participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Roles

### Contributor

Anyone who contributes code, documentation, reviews, triage, design feedback, or
support. There is no formal process to become a contributor — open a pull request
or an issue. All contributors must follow the [Code of Conduct](CODE_OF_CONDUCT.md)
and sign off their commits per the [DCO](#developer-certificate-of-origin-dco).

### Reviewer

A contributor with a sustained track record who is trusted to review pull
requests in one or more areas (e.g. `area/watchers`, `area/sinks`,
`area/helm`). Reviewers are listed in [`MAINTAINERS.md`](MAINTAINERS.md) and
referenced from [`.github/CODEOWNERS`](.github/CODEOWNERS). Reviewer approval is
advisory; a maintainer merges.

### Maintainer

A contributor with write access who is responsible for the health of the project:
reviewing and merging pull requests, cutting releases, triaging issues,
shepherding the roadmap, and upholding governance and the Code of Conduct. The
current maintainers are listed in [`MAINTAINERS.md`](MAINTAINERS.md).

## Contribution ladder

```
Contributor  ──►  Reviewer  ──►  Maintainer
```

- **Contributor → Reviewer:** demonstrated quality contributions over time and
  domain familiarity. Nominated by a maintainer; confirmed by lazy consensus of
  the maintainers.
- **Reviewer → Maintainer:** a sustained record of reviews, releases-assistance,
  community support, and good judgment. Nominated by a maintainer; requires
  approval of a **supermajority (two-thirds) of existing maintainers**. New
  maintainers should, where possible, broaden the project's organizational
  diversity (see *Neutrality*).

## Adding and removing maintainers

- **Adding** a maintainer follows the Reviewer → Maintainer process above.
- **Stepping down:** a maintainer may resign at any time by opening a pull
  request that updates `MAINTAINERS.md` and `.github/CODEOWNERS`.
- **Removing:** an inactive maintainer (no substantive activity for ~6 months) or
  one in serious violation of the Code of Conduct may be removed by a
  supermajority vote of the *other* maintainers. Removal for inactivity is not a
  judgment of character — emeritus maintainers are listed and welcomed back.

## Decision making

alertkube uses **lazy consensus**. Most decisions are made through the normal
pull-request and issue process: a change is proposed, anyone may comment, and if
there are no sustained objections it proceeds once it has the required approvals.

- **Routine changes** (bug fixes, docs, dependency bumps, additive features
  consistent with the roadmap): one maintainer approval (or one reviewer approval
  plus one maintainer merge) and green CI.
- **Substantial changes** (new public config surface, breaking changes, new
  external dependencies, anything affecting the security or determinism posture):
  open an issue or a short design doc / ADR first. Requires agreement of a
  majority of maintainers and a minimum 72-hour comment window.
- **When consensus fails:** a maintainer may call a vote. Each maintainer has one
  vote; a simple majority of all maintainers decides, except where a
  supermajority is specified above. Votes happen in the open on the relevant
  issue or pull request.

## Architecture decisions

Significant technical decisions are recorded as Architecture Decision Records
under [`docs/decisions/`](docs/decisions/). Proposing a new ADR is the preferred
way to drive a substantial change.

## Releases

Any maintainer may cut a release. The process is documented in
[`MAINTAINERS.md`](MAINTAINERS.md#releasing). Releases are tagged `vX.Y.Z`,
trigger the signed multi-arch image + Helm chart pipeline, and are recorded in
[`CHANGELOG.md`](CHANGELOG.md). The project follows
[Semantic Versioning](https://semver.org/).

## Developer Certificate of Origin (DCO)

All commits must be signed off (`git commit -s`), certifying agreement with the
[Developer Certificate of Origin](https://developercertificate.org/). This is
enforced by a CI check. See [`CONTRIBUTING.md`](CONTRIBUTING.md#developer-certificate-of-origin-dco).

## Code of Conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
Enforcement responsibility rests with the maintainers; project-agnostic incidents
may be escalated to the CNCF Code of Conduct Committee.

## Intellectual property and licensing

alertkube is licensed under [Apache-2.0](LICENSE). All contributions are
accepted under that license via the DCO; the project does not require a separate
CLA. The maintainers intend, should alertkube be accepted into a foundation
(e.g. CNCF Sandbox), to transfer the project's trademark, domain, and
repository ownership to that foundation as required.

## Amending this document

Changes to this document follow the *substantial change* process: a pull request,
a 72-hour comment window, and majority maintainer approval.
