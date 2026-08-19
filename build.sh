#!/usr/bin/env bash
# Build claumon with a version string that names the upstream release this fork
# is based on, plus the local commit.
#
# The version matters beyond cosmetics: the dashboard's update check compares it
# against upstream's latest release tag. A bare "nirvana-<sha>" never matches, so
# the dashboard shows a permanent "update available" badge that cannot be
# cleared. Semver build metadata ("0.20.0+nirvana.<sha>") is outside version
# precedence, so a fork of the current release reads as up to date while a real
# upstream release still registers.
set -euo pipefail

cd "$(dirname "$0")"
git fetch upstream --tags --quiet 2>/dev/null || true

BASE="$(git describe --tags --abbrev=0 upstream/main 2>/dev/null | sed 's/^v//')"
BASE="${BASE:-0.0.0}"
SHA="$(git rev-parse --short HEAD)"
DIRTY=""
git diff --quiet || DIRTY=".dirty"

VERSION="${BASE}+nirvana.${SHA}${DIRTY}"
OUT="${1:-claumon}"

echo "building ${VERSION} -> ${OUT}"
go build -ldflags "-X main.version=${VERSION}" -o "${OUT}" .
