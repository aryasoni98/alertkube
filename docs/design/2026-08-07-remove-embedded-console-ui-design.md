# Remove embedded console UI (residual cleanup)

- **Status:** Approved design
- **Date:** 2026-08-07
- **Deciders:** maintainers

## Context

alertkube previously shipped an embedded operator console under `internal/ui`,
served from the Go binary, plus related SSE/event plumbing. Commit `81d3956`
removed the console package, SSE `/api/events`, and `/api/config/render`, and
refreshed the marketing site under `web/`.

Operators use the token-gated HTTP API (`/api/alerts`, `/api/silences`, etc.).
The public site remains the GitHub Pages landing page (`web/`) with MkDocs
manual built into `web/manual` by `.github/workflows/pages.yml`.

One tracked leftover remains: `internal/ui/dist/logo.png` (empty package tree).

## Goal

Finish removing the embedded operator console artifacts while **keeping**:

- the marketing landing page (`web/`)
- the HTTP control-plane APIs in `internal/app/console.go`
- MkDocs docs hosting as deployed today

## Non-goals

- Deleting or redesigning `web/` (landing, changelog, assets)
- Removing or narrowing `/api/*` read/write endpoints
- Changing GitHub Pages URL layout (`/` landing, `/manual/` docs)
- Rewriting historical CHANGELOG / changelog-data mentions of the old console

## Approach

Residual cleanup only (Approach 1 from design discussion):

1. Delete `internal/ui/dist/logo.png` and the empty `internal/ui/` directory so
   `git ls-files internal/ui` is empty.
2. Confirm no remaining Go references to the UI package, SSE events handler, or
   `/api/config/render` (expected already absent after `81d3956`).
3. Leave historical release notes that mention the embedded console intact.

## Verification

- `git ls-files internal/ui` → no output
- `rg 'internal/ui|/api/events|config/render' --glob '*.go'` → no matches
- `go test ./internal/...` passes

## Success criteria

- No `internal/ui` tree in the repository
- Landing page and HTTP APIs unchanged in behavior
- Docs/Pages workflow untouched
