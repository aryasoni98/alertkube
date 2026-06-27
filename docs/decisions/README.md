# Architecture Decision Records

This directory holds **Architecture Decision Records (ADRs)** — short documents
that capture a significant technical or project decision, the context that forced
it, and its consequences. They are the durable memory of *why* alertkube is built
the way it is.

We use a lightweight [MADR](https://adr.github.io/madr/)-style format. See
[`template.md`](template.md).

## Conventions

- Files are named `NNNN-short-title.md` with a zero-padded sequence number.
- An ADR is immutable once `Accepted`. To change a decision, write a new ADR that
  `Supersedes` the old one and mark the old one `Superseded by NNNN`.
- Proposing an ADR is the preferred way to drive a *substantial change* under
  [`GOVERNANCE.md`](../../GOVERNANCE.md#decision-making).

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-client-go-over-controller-runtime.md) | Use client-go directly instead of controller-runtime | Accepted |
| [0002](0002-mkdocs-material-for-docs-site.md) | MkDocs Material for the documentation site | Accepted |
| [0003](0003-configmap-state-backend.md) | ConfigMap as the state-persistence backend (for now) | Accepted |
| [0004](0004-opt-in-silence-crd-via-dynamic-informer.md) | Opt-in Silence CRD via a dynamic informer (no controller-runtime) | Accepted |
