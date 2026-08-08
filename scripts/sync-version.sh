#!/usr/bin/env bash
# Propagate the release version from .release-please-manifest.json to every
# user-facing location: Helm chart, landing page (web UI), README, docs,
# build helpers, and SECURITY-INSIGHTS.
#
# Usage:
#   scripts/sync-version.sh              # sync from manifest → codebase
#   scripts/sync-version.sh --check      # verify (CI); exit 1 on drift
#   scripts/sync-version.sh 1.3.0          # set manifest, then sync
#   scripts/sync-version.sh --set 1.3.0 --date 2026-07-04
#
# Just shortcuts:
#   just sync-version
#   just sync-version 1.3.0
#   just sync-version version=1.3.0 date=2026-07-04
#   just version-check
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${ROOT}/.release-please-manifest.json"
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'

CHECK=0
SET_VERSION=""
SET_DATE=""

usage() {
  cat <<'EOF'
sync-version - keep Helm, landing page, README, and docs on one version.

  sync-version.sh [--check] [--set VERSION] [--date YYYY-MM-DD] [VERSION]

Examples:
  just sync-version                    # apply manifest version everywhere
  just sync-version 1.3.0              # bump manifest + sync
  just sync-version version=1.3.0 date=2026-07-04
  just version-check                   # CI drift gate
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) CHECK=1; shift ;;
    --set) SET_VERSION="${2#v}"; shift 2 ;;
    --date) SET_DATE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) SET_VERSION="${1#v}"; shift ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required tool: $1" >&2
    exit 1
  }
}

need jq

validate_semver() {
  local v="$1"
  [[ "$v" =~ $SEMVER_RE ]] || {
    echo "invalid semver: $v (expected MAJOR.MINOR.PATCH)" >&2
    exit 1
  }
}

read_manifest() {
  jq -r '."."' "$MANIFEST"
}

write_manifest() {
  local v="$1"
  local tmp
  tmp="$(mktemp)"
  jq --arg v "$v" '.["."] = $v' "$MANIFEST" >"$tmp"
  mv "$tmp" "$MANIFEST"
}

if [[ -n "$SET_VERSION" ]]; then
  validate_semver "$SET_VERSION"
  write_manifest "$SET_VERSION"
fi

VERSION="$(read_manifest)"
validate_semver "$VERSION"
VTAG="v${VERSION}"

if [[ -n "$SET_DATE" && ! "$SET_DATE" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "invalid --date (expected YYYY-MM-DD): $SET_DATE" >&2
  exit 1
fi

# --- check helpers -----------------------------------------------------------
expect_line() {
  local file="$1"
  local pattern="$2"
  local label="$3"
  local got
  got="$(grep -E "$pattern" "$file" | head -1 || true)"
  if [[ -z "$got" ]]; then
    echo "FAIL $label: no match for /$pattern/ in ${file#"$ROOT"/}" >&2
    return 1
  fi
  if ! grep -Eq "$pattern" "$file"; then
    echo "FAIL $label: ${file#"$ROOT"/}" >&2
    return 1
  fi
}

expect_eq() {
  local label="$1"
  local want="$2"
  local got="$3"
  if [[ "$want" != "$got" ]]; then
    echo "FAIL $label: want '$want' got '$got'" >&2
    return 1
  fi
}

run_check() {
  local ok=0

  expect_eq "manifest" "$VERSION" "$(read_manifest)" || ok=1

  local ak got_ak html chart_ver app_ver img_tag build_tag sec_rel

  got_ak="$(grep -oE 'const AK_VERSION = "v[0-9]+\.[0-9]+\.[0-9]+(-[^"]+)?"' \
    "$ROOT/web/ak-lib.jsx" | sed -E 's/.*"(v[^"]+)".*/\1/')"
  expect_eq "web/ak-lib.jsx AK_VERSION" "$VTAG" "$got_ak" || ok=1

  html="$(grep -oE '"softwareVersion": "[0-9]+\.[0-9]+\.[0-9]+(-[^"]+)?"' \
    "$ROOT/web/index.html" | sed -E 's/.*"([0-9.+-]+)".*/\1/')"
  expect_eq "web/index.html softwareVersion" "$VERSION" "$html" || ok=1

  chart_ver="$(grep -E '^version:' "$ROOT/helm/Chart.yaml" | awk '{print $2}')"
  app_ver="$(grep -E '^appVersion:' "$ROOT/helm/Chart.yaml" | awk '{print $2}' | tr -d '"')"
  expect_eq "helm/Chart.yaml version" "$VERSION" "$chart_ver" || ok=1
  expect_eq "helm/Chart.yaml appVersion" "$VERSION" "$app_ver" || ok=1

  img_tag="$(grep -oE 'alertkube:v[0-9]+\.[0-9]+\.[0-9]+(-[^" ]+)?' "$ROOT/helm/Chart.yaml" \
    | head -1 | sed 's/.*://')"
  expect_eq "helm/Chart.yaml artifacthub image tag" "$VTAG" "$img_tag" || ok=1

  build_tag="$(grep -oE 'TAG="\$\{TAG:-v[0-9]+\.[0-9]+\.[0-9]+(-[^"]+)?\}"' "$ROOT/scripts/build.sh" \
    | sed -E 's/.*\$\{TAG:-([^}]+)\}.*/\1/')"
  expect_eq "scripts/build.sh default TAG" "$VTAG" "$build_tag" || ok=1

  sec_rel="$(grep -E '^  project-release:' "$ROOT/SECURITY-INSIGHTS.yml" \
    | sed -E 's/.*project-release: (v[^ #]+).*/\1/')"
  expect_eq "SECURITY-INSIGHTS.yml project-release" "$VTAG" "$sec_rel" || ok=1

  grep -Fq "[${VTAG}]" "$ROOT/README.md" || {
    echo "FAIL README.md latest release link missing ${VTAG}" >&2
    ok=1
  }
  grep -Fq -- "--version ${VERSION}" "$ROOT/README.md" || {
    echo "FAIL README.md helm --version ${VERSION}" >&2
    ok=1
  }
  grep -Fq "alertkube:${VTAG}" "$ROOT/README.md" || {
    echo "FAIL README.md docker image tag ${VTAG}" >&2
    ok=1
  }

  grep -Fq "current \`${VTAG}\`" "$ROOT/docs/docs/index.md" || {
    echo "FAIL docs/docs/index.md current ${VTAG}" >&2
    ok=1
  }

  local tutorial
  for tutorial in "$ROOT"/docs/docs/tutorials/*.md; do
    [[ -f "$tutorial" ]] || continue
    grep -Fq -- "--version ${VERSION}" "$tutorial" || {
      echo "FAIL ${tutorial#"$ROOT"/} helm --version ${VERSION}" >&2
      ok=1
    }
  done

  if [[ "$ok" -ne 0 ]]; then
    echo >&2
    echo "Version drift detected. Run: just sync-version" >&2
    exit 1
  fi

  echo "OK all version strings match manifest ${VERSION} (${VTAG})"
}

# --- apply updates -----------------------------------------------------------
apply_sync() {
  export VERSION VTAG
  export SET_DATE

  perl -i -pe '
    my $v = $ENV{VERSION};
    my $vt = $ENV{VTAG};
    s/^version: .*/version: $v/;
    s/^appVersion: .*/appVersion: $v/;
    s|image: ghcr.io/aryasoni98/alertkube:v[0-9]+\.[0-9]+\.[0-9]+(-[^"\s]+)?|image: ghcr.io/aryasoni98/alertkube:$vt|;
  ' "$ROOT/helm/Chart.yaml"

  perl -i -pe '
    my $v = $ENV{VERSION};
    my $vt = $ENV{VTAG};
    s/const AK_VERSION = "v[0-9]+\.[0-9]+\.[0-9]+(-[^"]*)?"; \/\/ x-release-please-version/const AK_VERSION = "$vt"; \/\/ x-release-please-version/;
    if ($ENV{SET_DATE}) {
      s/const AK_VERSION_DATE = "[0-9]{4}-[0-9]{2}-[0-9]{2}";/const AK_VERSION_DATE = "$ENV{SET_DATE}";/;
    }
  ' "$ROOT/web/ak-lib.jsx"

  perl -i -pe '
    my $v = $ENV{VERSION};
    s/"softwareVersion": "[0-9]+\.[0-9]+\.[0-9]+(-[^"]*)?", <!-- x-release-please-version -->/"softwareVersion": "$v", <!-- x-release-please-version -->/;
    if ($ENV{SET_DATE}) {
      s/"datePublished": "[0-9]{4}-[0-9]{2}-[0-9]{2}"/"datePublished": "$ENV{SET_DATE}"/;
    }
  ' "$ROOT/web/index.html"

  perl -i -pe '
    my $vt = $ENV{VTAG};
    s/TAG="\$\{TAG:-v[0-9]+\.[0-9]+\.[0-9]+(-[^"]*)?\}" # x-release-please-version/TAG="\${TAG:-$vt}" # x-release-please-version/;
  ' "$ROOT/scripts/build.sh"

  perl -i -pe '
    my $vt = $ENV{VTAG};
    s/^  project-release: v[0-9]+\.[0-9]+\.[0-9]+(-[^ #]*)? # x-release-please-version/  project-release: $vt # x-release-please-version/;
  ' "$ROOT/SECURITY-INSIGHTS.yml"

  perl -i -pe '
    my $v = $ENV{VERSION};
    my $vt = $ENV{VTAG};
    s/\[v[0-9]+\.[0-9]+\.[0-9]+(-[^\]]+)?\]\(https:\/\/github.com\/aryasoni98\/alertkube\/releases\/latest\)/[$vt](https:\/\/github.com\/aryasoni98\/alertkube\/releases\/latest)/;
    s/--version [0-9]+\.[0-9]+\.[0-9]+(-[^\s\\]+)?/--version $v/g;
    s|ghcr.io/aryasoni98/alertkube:v[0-9]+\.[0-9]+\.[0-9]+(-[^\s\\]+)?|ghcr.io/aryasoni98/alertkube:$vt|g;
  ' "$ROOT/README.md"

  perl -i -pe '
    my $vt = $ENV{VTAG};
    s/current `v[0-9]+\.[0-9]+\.[0-9]+(-[^`]+)?`/current `$vt`/;
  ' "$ROOT/docs/docs/index.md"

  local tutorial
  for tutorial in "$ROOT"/docs/docs/tutorials/*.md; do
    [[ -f "$tutorial" ]] || continue
    perl -i -pe '
      my $v = $ENV{VERSION};
      s/--version [0-9]+\.[0-9]+\.[0-9]+(-[^\s\\]+)?/--version $v/g;
    ' "$tutorial"
  done

  if command -v helm-docs >/dev/null 2>&1; then
    (cd "$ROOT" && helm-docs --chart-search-root=helm --template-files=README.md.gotmpl >/dev/null)
    echo "Regenerated helm/README.md (helm-docs)"
  else
    perl -i -pe '
      my $v = $ENV{VERSION};
      s/!\[Version: [0-9]+\.[0-9]+\.[0-9]+(-[^\]]+)?\]/![Version: $v]/g;
      s/AppVersion: [0-9]+\.[0-9]+\.[0-9]+(-[^\]]+)?/AppVersion: $v/g;
      s/--version [0-9]+\.[0-9]+\.[0-9]+(-[^\s\\]+)?/--version $v/g;
    ' "$ROOT/helm/README.md"
    echo "Updated helm/README.md (install helm-docs for full regen: just helm-docs)"
  fi
}

if [[ "$CHECK" -eq 1 ]]; then
  run_check
  exit 0
fi

apply_sync
run_check
echo "Synced ${VERSION} (${VTAG}) → Helm chart, landing page, README, docs, scripts/build.sh, SECURITY-INSIGHTS"
