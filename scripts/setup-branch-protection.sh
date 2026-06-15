#!/usr/bin/env bash
# Configure branch protection on the default branch to satisfy the OpenSSF
# Scorecard "Branch-Protection" check and enforce review + green CI before merge.
#
# Requires: gh CLI authenticated as a repo admin (`gh auth login`).
# Usage:    scripts/setup-branch-protection.sh [owner/repo] [branch]
# Defaults: repo = current `gh repo view`, branch = master
#
# This is intentionally a script (not a workflow): branch protection needs admin
# rights a CI token should not hold. A maintainer runs it once.
set -euo pipefail

REPO="${1:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}"
BRANCH="${2:-master}"

echo "Configuring branch protection on ${REPO}@${BRANCH} ..."

# Required status checks: the job names that must pass. Adjust to match the
# 'name:' of each job you want to gate on.
read -r -d '' PAYLOAD <<'JSON' || true
{
  "required_status_checks": {
    "strict": true,
    "checks": [
      { "context": "Build & Test" },
      { "context": "golangci-lint" },
      { "context": "Check Signed-off-by" }
    ]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true,
    "required_approving_review_count": 1
  },
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true,
  "restrictions": null
}
JSON

echo "${PAYLOAD}" | gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  "/repos/${REPO}/branches/${BRANCH}/protection" \
  --input -

echo "Done. Verify in: Settings → Branches → Branch protection rules."
