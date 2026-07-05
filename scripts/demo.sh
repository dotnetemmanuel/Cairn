#!/usr/bin/env bash
# Arm demo mode and launch Cairn against built-in fake data for screenshots.
# No GitHub account, no gh, no git-town, no network. All data is synthetic
# (see internal/gh/demo.go). Read-only: nothing here mutates anything.
#
# Usage:
#   scripts/demo.sh            build, then launch demo mode
#   scripts/demo.sh --no-build launch the existing ./cairn binary in demo mode
#
# For the two git-backed shots (stack mode, conflict resolver) use
# scripts/demo-repo.sh, which seeds a throwaway repo whose branch names line up
# with the demo PR overlay, then launch this from inside that repo.
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ "${1:-}" != "--no-build" ]]; then
  echo "building cairn..."
  go build -o cairn .
fi

echo "launching demo mode (CAIRN_DEMO=1). press q to quit."
CAIRN_DEMO=1 ./cairn
