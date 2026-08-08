# Design documents

Longer-form design work that precedes implementation. These sit between an
issue (what problem) and an ADR (what we decided): they carry the exploration,
the alternatives considered, and the implementation plan.

Relationship to the other doc types:

| Where | What it is | Lifecycle |
| --- | --- | --- |
| [`docs/decisions/`](../decisions/) | ADRs — a single decision and its consequences | Permanent; superseded, never deleted |
| `docs/design/` (here) | Design + plan for a body of work | Kept after shipping as the record of *why it is shaped this way* |
| [`docs/docs/`](../docs/) | The user manual (Diátaxis) | Lives with the feature |

A design doc does not need to be updated once the work ships — it is a record
of the thinking at the time, not documentation of current behaviour. If it
turns out to be wrong, the correction belongs in an ADR or the manual.

## Index

| Document | Status |
| --- | --- |
| [Correlation engine — design](2026-07-10-correlation-engine-design.md) | In progress (`internal/topology` exists, not yet wired) |
| [Correlation engine — plan](2026-07-10-correlation-engine-plan.md) | In progress |
| [Remove embedded console UI](2026-08-07-remove-embedded-console-ui-design.md) | Shipped |
