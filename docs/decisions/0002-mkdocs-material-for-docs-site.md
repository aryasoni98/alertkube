# 0002. MkDocs Material for the documentation site

- **Status:** Accepted
- **Date:** 2026-06-15
- **Deciders:** maintainers

## Context and problem statement

alertkube has good reference docs (`README.md`, `docs/OPERATIONS.md`,
`docs/TROUBLESHOOTING.md`, `docs/MIGRATION-FROM-V1.md`) but no structured
documentation site. CNCF readiness expects a navigable docs site organized for
different reader needs. We need to pick a static-site generator and an
information architecture.

## Considered options

- **A. MkDocs Material.** Python, single `mkdocs.yml`, Markdown content,
  first-class search, admonitions, code annotations. Ubiquitous across CNCF
  projects.
- **B. Docusaurus 3.** React/Node, MDX, strong versioning and i18n.
- **C. Hugo (Docsy).** Fast, but heavier theming/templating overhead.

## Decision

Use **MkDocs Material (Option A)**, with content organized per the
[Diátaxis](https://diataxis.fr/) framework: **Tutorials**, **How-to guides**,
**Reference**, **Explanation**.

Rationale: fastest path to a maintainable site, all content stays plain Markdown
(reusable, low contributor friction), and it is the de-facto standard in the
cloud-native ecosystem. Versioning/i18n needs (which would favor Docusaurus) are
not yet pressing; if they become so, this ADR can be superseded.

## Consequences

### Positive

- Content authored as plain Markdown under `docs-site/docs/`; contributors need
  no JS toolchain.
- `mkdocs serve` gives instant local preview; `mkdocs build --strict` catches
  broken links/nav in CI.
- Diátaxis gives a clear home for every page and prevents "one giant README".

### Negative / trade-offs

- Built-in doc **versioning** needs the extra `mike` plugin (deferred until we
  have multiple supported majors).
- **i18n** is weaker than Docusaurus; revisit if Phase 3 translations become a
  priority.
- The existing `docs/index.html` landing page and a MkDocs site cannot both be
  the repository's single GitHub Pages site - hosting must be decided
  (subpath, separate Pages source, or external host). Tracked as a follow-up.

### Follow-ups / triggers to revisit

- **Trigger:** need for versioned docs across multiple supported releases → add
  `mike`, or reconsider Docusaurus.
- **Trigger:** sustained demand for translated docs → reconsider Docusaurus.
- Decide final hosting for the MkDocs site vs the marketing landing page.
