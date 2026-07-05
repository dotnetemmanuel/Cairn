#!/usr/bin/env bash
# Seed a throwaway git repo whose branches match the demo PR overlay, for the two
# git-backed screenshots: stack mode (#4) and the conflict resolver (#5). These
# screens run real git-town/git against the current directory, so fixtures in
# internal/gh do not reach them — the tree must be a real repo. The branch names
# (search-api -> search-index -> search-ui) line up with the demo gh stubs, so the
# PR-number/CI overlay renders on top of the real tree.
#
# Idempotent: nukes and recreates the repo each run.
#
# Usage:
#   scripts/demo-repo.sh                 seed the stack with amber drift (for #4)
#   scripts/demo-repo.sh --conflict      also stage a conflict (for #5)
#
# Then, from the repo:
#   cd /tmp/northwind-atlas
#   CAIRN_DEMO=1 <path-to>/cairn        press S for stack mode; the tree is real,
#                                       the PR/CI overlay comes from the demo stubs.
#   for #5: trigger a restack/sync so the seeded conflict fires; the resolver opens.
set -euo pipefail

REPO=/tmp/northwind-atlas
WITH_CONFLICT=0
[[ "${1:-}" == "--conflict" ]] && WITH_CONFLICT=1

rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"

git init -q -b main
git config user.name "Avery"
git config user.email "avery@northwind.example"

# git-town lineage config: same keys Cairn reads (see internal/townie InitArgv +
# SetParent). Local .git/config only; nothing is pushed.
git config git-town.main-branch main
git config git-town.sync-feature-strategy rebase

seed() { printf '%s\n' "$2" > "$1"; git add "$1"; }

# main (trunk)
seed README.md "# northwind/atlas
Internal search platform."
seed search.go "package search

// Query runs a search over the index.
func Query(q string) []int { return nil }"
git commit -qm "Seed the search platform"

# search-api -> search-index -> search-ui, each a real commit with recorded parent
git checkout -q -b search-api
seed api.go "package search

// API exposes the search endpoint.
func API() string { return \"/search\" }"
git commit -qm "Add the search API skeleton"
git config git-town-branch.search-api.parent main

git checkout -q -b search-index
seed index.go "package search

// Index is an in-memory inverted index.
type Index struct{ postings map[string][]int }"
git commit -qm "Build the inverted index"
git config git-town-branch.search-index.parent search-api

git checkout -q -b search-ui
seed ui.go "package search

// UI renders the search box.
func UI() string { return \"<input>\" }"
git commit -qm "Wire the search UI"
git config git-town-branch.search-ui.parent search-index

# Amber drift for #4: amend search-api WITHOUT restacking its descendants, so
# search-index's recorded parent no longer matches its actual merge-base. Cairn
# flags it amber with the drift warning.
git checkout -q search-api
seed api.go "package search

// API exposes the search endpoint (v2, with pagination).
func API() string { return \"/search?page=1\" }"
git commit -q --amend -m "Add the search API skeleton (paginated)"

if [[ "$WITH_CONFLICT" == 1 ]]; then
  # Stage a conflict for #5: main and search-api edit the SAME line, so a
  # restack/sync of the stack onto main must stop to resolve it.
  git checkout -q main
  seed api.go "package search

// API exposes the search endpoint (trunk hotfix).
func API() string { return \"/search?v=trunk\" }"
  git add api.go 2>/dev/null || true
  git commit -qm "Hotfix the API path on trunk"
fi

git checkout -q search-index

echo "seeded $REPO"
echo "  branches: main -> search-api -> search-index -> search-ui (amber drift on search-api)"
[[ "$WITH_CONFLICT" == 1 ]] && echo "  conflict staged: restack/sync will stop on api.go"
echo
echo "next:"
echo "  cd $REPO"
echo "  CAIRN_DEMO=1 $(cd "$OLDPWD" && pwd)/cairn   # press S for stack mode"
