# Cairn — Build Plan & Implementation Spec

> A terminal cockpit for GitHub: dashboard + review + stacked-PR workflow in one TUI.
> Think **gh-dash** (the board) × **GitHub Desktop** (approachable git ops) × **git-town** (branch workflow) × **Graphite** (PR stacking & merging), running fully local.

**Cairn** — a cairn is a stack of balanced stones used as a trail marker. It carries three meanings at once: a *stack* (the core feature), a *waypoint you navigate by* (the cockpit), and stone-balancing as a *mindful* practice (a nod to Mindful Stack). Binary and module are `cairn`. The stack-tree view is rendered as a literal cairn — each PR a stone resting on the one below.

---

## 0. How to use this document with Claude Code

This is a phased spec. Execute **one phase at a time**, in order. Each phase ends in something runnable with explicit acceptance criteria — verify those before moving on. Do not skip ahead; later phases assume earlier invariants hold.

Suggested kickoff prompt to Claude Code:

> "Read `cairn-build-plan.md`. Implement Phase 0 only. Stop at the acceptance criteria and show me how to run it. Do not start Phase 1."

Repeat per phase. Keep the plan in the repo root as the source of truth; update the "Status" checkboxes as phases land.

### ⛔ Hard rules — non-negotiable, read before every phase

These override any local implementation decision. If a phase seems to require breaking one, **stop and ask the maintainer** rather than working around it.

1. **DO NOT implement rebasing, restacking, or lineage tracking yourself.** This is the single most important rule. Every stack mutation — `sync`, `restack`, `new`/`insert` (append/prepend), `ship` — **shells out to the `git-town` binary.** Cairn only *reads* lineage from git config; **git-town writes it.** If you find yourself writing `git rebase`, `cherry-pick`, merge-base math, or any logic to keep branches stacked, you have gone off-spec — delete it and delegate to git-town. Reimplementing git-town is explicitly out of scope and will not be accepted. (Full rationale in §4.)
2. **DO NOT manage auth tokens.** Use `go-gh`, which reads the existing `gh` token. Never read/write/store a token yourself. (§5)
3. **Reads → GraphQL, writes → REST.** Don't invent a different data path. (§5)
4. **Unknown subcommands fall through to `git`.** Never intercept or block commands Cairn doesn't define. (§6)
5. **No server-side merge queue.** Client-side bottom-up merge only in v1. (§1 non-goals)

If you are about to write code that maintains stack relationships, that is rule 1 firing — re-read §4 and delegate instead.

---

## 1. What we're building (and what we're not)

**Goal:** a keyboard-driven TUI that lets you (1) see the PRs/issues/CI you care about across an org, (2) review and act on them without opening a browser, and (3) author, sync, and merge **stacked pull requests** with first-class commands.

**Core differentiators** (the reasons this should exist rather than just using gh-dash):
1. A **live stack tree** — a visual graph of the current stack with per-branch PR status and CI dots. Neither gh-dash nor GitHub Desktop has this.
2. **In-TUI review** — diff + comment + approve in-pane, so you stop bouncing to github.com.
3. **`ship`** — merge a reviewed stack bottom-up, auto-retargeting bases, without the Graphite web app.

**Non-goals (v1):**
- No server-side merge queue (atomic "merge the whole green stack" needs server cooperation — out of scope; we do client-side bottom-up merging instead).
- No hosting/self-hosted backend. Fully local, single binary.
- No reimplementation of git's rebase engine. We delegate stack mechanics to **git-town** (see §4).
- Not a general git client. Raw git stays raw git; unknown `cairn` subcommands fall through to `git`.

---

## 2. Tech stack decision

### Primary recommendation: **Go**

Rationale, specific to this project:
- **Ecosystem fit is decisive.** The two tools we lean on are Go: `gh-dash` (reference implementation for the board/review TUI) and `git-town` (the lineage/sync/ship engine). Cribbing from gh-dash and integrating git-town is far smoother in-language.
- **Best-in-class TUI framework.** Bubble Tea + Lip Gloss + Bubbles (the Charm stack) is what gh-dash uses; mature, well-documented, lots of prior art.
- **Official GitHub auth/client library.** `github.com/cli/go-gh/v2` reuses the token from the user's existing `gh auth login`, and provides both REST and GraphQL clients. Zero token management.
- **Distribution.** Single static binary → trivial AUR packaging, no runtime dependency. Matches an Arch/Omarchy workflow.

**Note on language familiarity:** the maintainer writes C#/TS, not Go. Go is a small language and the jump from C#/TS is days, not weeks. Since Claude Code is doing the bulk of the authoring, library availability and reference implementations matter more than existing fluency — which is exactly why Go wins here.

### Locked library choices (Go)

| Concern | Library |
|---|---|
| TUI runtime | `github.com/charmbracelet/bubbletea` |
| Styling/layout | `github.com/charmbracelet/lipgloss` |
| Widgets (list, viewport, textinput, spinner) | `github.com/charmbracelet/bubbles` |
| GitHub auth + REST + GraphQL | `github.com/cli/go-gh/v2` |
| Typed GraphQL (optional, if hand-written queries get unwieldy) | `github.com/shurcooL/githubv4` |
| Syntax highlighting in diffs | `github.com/alecthomas/chroma/v2` |
| Config | `gopkg.in/yaml.v3` (+ a JSON-schema for editor autocomplete) |
| Logging | `log/slog` (stdlib) |

### Fallback: **TypeScript** (only if Go is rejected)

Viable because Graphite's own CLI is TS. Stack would be **Ink** (React-for-CLI) + **Octokit** + shelling out to `git-town`. Costs: heavier runtime/distribution, no in-language gh-dash reference, weaker large-diff performance. Choose only if the maintainer would otherwise not ship at all. **Do not mix** — pick one and commit.

---

## 3. Architecture

```
┌─────────────────────────────────────────────────────────┐
│  TUI (Bubble Tea)                                         │
│  ┌───────────┬────────────────────┬───────────────────┐  │
│  │ Sections  │  Review / Diff     │  Detail / Checks  │  │
│  │ + Stack   │  (viewport +       │  (conversation,   │  │
│  │   Tree    │   chroma)          │   CI, conflicts)  │  │
│  └───────────┴────────────────────┴───────────────────┘  │
├─────────────────────────────────────────────────────────┤
│  Command layer  (new/amend/restack/sync/propose/ship...)  │
├──────────────┬───────────────────────┬───────────────────┤
│ GitHub client│  Stack engine         │  Git ops          │
│ (go-gh:      │  (delegates to        │  (shell out to    │
│  GraphQL     │   git-town CLI +      │   `git` for       │
│  reads,      │   reads lineage from  │   diff/status)    │
│  REST writes)│   git config)         │                   │
└──────────────┴───────────────────────┴───────────────────┘
```

**Key principle:** the TUI never does git mechanics directly. It issues intents to the command layer, which orchestrates the GitHub client, the stack engine, and git ops. This keeps the scary rebase logic in one place (git-town) and makes the command layer unit-testable.

---

## 4. The stack/lineage model (the hard part — read carefully)

GitHub has **no concept of a stack**. A "stack" is a client-side illusion: a chain of branches where each PR's base is the branch below it instead of `main`, plus metadata recording parent relationships. The entire difficulty of this project lives in keeping that metadata true.

**Decision: delegate lineage + restack + sync + ship to `git-town`. Do not reimplement.**

- git-town already stores branch lineage in **git config** under keys like `git-town-branch.<name>.parent`. `cairn` **reads that config directly** to build the stack tree (fast, no parsing of CLI output needed for display).
- git-town already handles the rebase-heavy operations safely: `git town sync` (pull trunk + rebase the whole stack + prune merged), `git town append`/`prepend`/`set-parent` (lineage edits), `git town ship` (merge a branch + retarget children + clean up), `git town propose` (open PR).
- `cairn` **shells out** to the `git-town` binary for these mutations. Importing git-town as a package is rejected: its internals are not a stable public API. Shelling out to the CLI is the stable integration boundary.
- `cairn` requires `git-town` and `gh` to be installed; check for both at startup and emit a clear error if missing.

**The two landmines, and how we handle them:**
1. **Base retargeting on merge.** When the bottom PR merges, the next PR's base must move from the merged branch to the new trunk position. `git town ship` handles the local lineage; for the GitHub-side base change we call the REST "update pull request" endpoint to set the new base. Verify this explicitly in Phase 5 tests.
2. **User runs raw git behind our back.** After any external git operation, lineage in config may be stale. `cairn` must offer a `restack`/`sync` that reconciles, and the stack-tree view must detect "branch X is not actually on top of its recorded parent" and flag it visually (amber node).

---

## 5. GitHub API strategy

- **Auth:** none of our own. Use `go-gh` which resolves the token from `gh auth status`. Document that the user runs `gh auth login` and `gh auth refresh -s read:org,workflow` (org repos need `read:org`; Actions need `workflow`).
- **Reads → GraphQL.** One query fills an entire board section (PRs + review state + checks rollup + labels). Batch aggressively to stay under rate limits. Cache responses with a short TTL and a manual refresh key.
- **Writes → REST (via go-gh) or `gh`.** Approve, comment, merge, update-base, re-run checks. Mutations are lower-volume; clarity beats cleverness here.
- **Rate limits:** surface remaining quota in a status line. Back off and show a clear message rather than hammering.

---

## 6. Command vocabulary

Every verb is both a **TUI keybinding** and a **CLI subcommand** (`cairn <verb>`). Unknown subcommands fall through to `git` (so `cairn status` == `git status`).

### Authoring a stack
| Verb | Does | Delegates to |
|---|---|---|
| `new <name>` | branch off current, stage, commit (1 change = 1 PR) | `git town append` + `git commit` |
| `amend` | fold staged changes into current branch, restack descendants | `git commit --amend` + `git town sync --no-push` (or restack) |
| `insert <name>` | wedge a new change into the middle of the stack | `git town prepend` |
| `split` | interactively carve a fat branch into a stack by hunk | custom (uses `git add -p` semantics) — **hardest authoring feature, defer to Phase 6** |
| `fold` | collapse current branch into its parent | custom + lineage fixup |

### Navigation / view
| Verb | Does |
|---|---|
| `up` / `down` | move to child / parent branch |
| `top` / `bottom` | jump to ends of the stack |
| `tree` | render the stack graph (the centerpiece view) |
| `dash` | open the section board |

### Maintenance
| Verb | Does | Delegates to |
|---|---|---|
| `sync` | fetch trunk, rebase whole stack, prune merged, flag conflicts | `git town sync` |
| `restack` | re-rebase descendants after a manual edit | `git town sync --no-push` / lineage reconcile |

### PR lifecycle
| Verb | Does | Delegates to |
|---|---|---|
| `propose` | push stack, open/update each PR with correct base | `git town propose` per branch |
| `review` | open in-pane diff + comment/approve on selected PR | go-gh REST |
| `checks` | live CI status; tail a failed job; re-run | go-gh / `gh run` |
| `ship` | merge bottom-up, retarget next base, prune, advance | `git town ship` + REST update-base |

---

## 7. TUI layout

Three panes, vim-style navigation, rebindable keys, `?` help overlay.

- **Left — Sections + Stack Tree.** Top: configurable sections (My PRs / Needs Review / org views / Notifications) using GitHub search filters. Bottom: the live stack tree for the current repo — each node shows branch name, PR number, review state, and a CI dot (green/red/amber/grey). Amber node = lineage drift (see §4).
- **Center — Review / Diff.** Scrollable, syntax-highlighted (chroma) diff with file tree. Hunk-level navigation. Comment composer (GitHub-flavored Markdown), approve / request-changes inline.
- **Right — Detail / Checks / Conflicts.** PR conversation, checks list, and during `sync`/`restack` the **conflict resolver** (this is where we borrow GitHub Desktop's approachable model — ours/theirs/edit, file-by-file).

**Config** lives at `~/.config/cairn/config.yml`: sections (with GitHub-search-syntax filters), `repoPaths` (branch→local path), theme, keybindings, default trunk. Ship a JSON schema for editor autocomplete. Borrow gh-dash's config shape where sensible — users migrating from it should feel at home.

---

## 8. Build phases

> Status legend: `[ ]` todo · `[~]` in progress · `[x]` done

### Phase 0 — Scaffolding `[x]`
**Goal:** runnable Bubble Tea skeleton that authenticates and prints the current user.
- Init Go module; add bubbletea, lipgloss, bubbles, go-gh.
- Startup checks: `git`, `git-town`, `gh` present; `gh` authenticated. Clear errors if not.
- Load `~/.config/cairn/config.yml` (with sane defaults if absent).
- Minimal Bubble Tea app: a header showing the authenticated GitHub login + rate-limit remaining; `q` quits.
**Acceptance:** `cairn` launches, shows `Logged in as <login> · <n> API calls remaining`, quits cleanly. `cairn doctor` reports tool/auth status.

### Phase 1 — Read-only dashboard `[ ]`
**Goal:** the gh-dash core. Sections of PRs/issues from GraphQL.
- Config-driven sections (filters in GitHub search syntax).
- GraphQL query per section → list view (Bubbles `list`), columns: repo, number, title, author, review state, CI rollup, updated.
- `tab`/`shift-tab` cycle sections; `j/k` navigate; `r` refresh; loading spinners; progressive render.
- Notifications section via the notifications API.
**Acceptance:** with a real config, `cairn` shows live "My PRs", "Needs Review", and an org section, refreshable, no browser needed to *see* state.

### Phase 2 — PR detail + review pane `[ ]`
**Goal:** review without leaving the terminal.
- Selecting a PR opens center pane: syntax-highlighted diff (chroma), file tree, hunk navigation.
- Right pane: conversation + checks list.
- Actions: comment (Markdown composer, submit), approve, request-changes, open-in-browser as escape hatch.
**Acceptance:** can read a real PR's diff, leave a comment, and approve it entirely from the TUI; the action reflects on github.com.

### Phase 3 — Stack engine read path + stack tree `[ ]`
**Goal:** see the stack. No mutations yet.
- Read git-town lineage from git config; build an in-memory stack model for the current repo.
- Render the **stack tree** in the left pane: nodes with branch, PR#, review state, CI dot.
- Drift detection: compare recorded parent vs actual merge-base; flag amber.
- `up`/`down`/`top`/`bottom` navigation maps selection to branches.
**Acceptance:** in a repo with an existing git-town stack, the tree renders correctly and flags an artificially-drifted branch amber.

### Phase 4 — Stack authoring + maintenance `[ ]`
**Goal:** create and maintain stacks.

> ⛔ **Hard rule 1 applies here most of all.** Every operation below is a thin wrapper that shells out to `git-town` + `git`. You are writing orchestration and UI, *not* a rebase engine. If you reach for merge-base/rebase logic, stop — delegate to git-town.

- Implement `new`, `amend`, `insert`, `sync`, `restack` by delegating to git-town + git (see §6 mapping).
- Wrap each in TUI keybindings with confirmation + live output streaming into the right pane.
- After every mutation, reload lineage and re-render the tree.
**Acceptance:** can build a 3-branch stack with `new` ×3, amend the bottom and watch the tree restack, and `sync` against an updated trunk — all from the TUI.

### Phase 5 — Propose + ship `[ ]`
**Goal:** the payoff — push and merge stacks.

> ⛔ **Hard rule 1 still applies.** `ship` is `git town ship` plus one REST call to retarget the next PR's base. Do not hand-roll the merge/retarget/cleanup sequence — git-town owns the local side.

- `propose`: push the stack, open/update each PR with the correct base branch.
- `ship`: merge bottom-up; after each merge, **retarget the next PR's base via REST**, prune the merged branch, advance selection.
- Guard rails: refuse to ship a branch whose PR isn't approved / checks aren't green (configurable override).
**Acceptance:** a reviewed 2-PR stack ships fully from the TUI; on github.com the second PR's base correctly moves to trunk after the first merges; no orphaned branches.

### Phase 6 — Conflict resolver, checks panel, `split`, polish `[ ]`
**Goal:** handle the scary moments and the power features.
- Conflict resolver UI during `sync`/`restack` (ours/theirs/edit, file-by-file).
- Live CI: tail a failing job's logs; re-run failed jobs.
- `split` (interactive hunk → new stacked branch) and `fold`.
- Theming — ship the **Event Horizon** theme as default (palette + token mapping in Appendix A), full keybinding customization, `?` help overlay.
**Acceptance:** a deliberately-conflicting `sync` is resolvable entirely in-TUI; `split` turns a 2-file branch into a 2-PR stack.

### Phase 7 — Packaging & docs `[ ]`
**Goal:** shippable.
- `goreleaser` config; static binaries for linux/amd64 + arm64.
- AUR `PKGBUILD` (`cairn-bin` and/or source package).
- README, config reference, the JSON schema, a `cairn doctor` that diagnoses setup.
**Acceptance:** `yay -S cairn-bin` (or local PKGBUILD) installs a working binary on a clean Arch box.

---

## 9. Risks & key decisions (decided)

- **Don't reimplement rebase/lineage.** Delegate to git-town. Revisit only if shell-out latency becomes a UX problem (it won't for interactive use).
- **No merge queue in v1.** Bottom-up client-side merge only. Document the limitation.
- **go-gh for auth** — never store our own tokens.
- **GraphQL for reads, REST for writes.** Don't fight it.
- **Lineage source of truth = git config (git-town's keys).** `cairn` reads, git-town writes.
- **Fall-through to git** for unknown subcommands — never block the user's muscle memory.

---

## 10. Testing

- **Unit:** command layer with the GitHub client and git/git-town calls behind interfaces (mock them). Lineage model + drift detection get real table tests.
- **Integration:** a throwaway test repo + a test GitHub org (or `gh`'s test fixtures). Script: build stack → propose → approve → ship → assert clean trunk + retargeted bases.
- **TUI:** Bubble Tea's `teatest` for golden-frame snapshots of each pane.
- **Manual smoke:** `cairn doctor` + a documented end-to-end checklist per release.

---

## 11. Glossary for the implementing agent

- **Trunk:** the integration branch (`main`/`master`). git-town knows it via `git town config`.
- **Stack:** ordered chain of branches, each based on the one below; each → one PR.
- **Restack:** rebase descendants after an ancestor changes.
- **Drift:** recorded parent ≠ actual git ancestor; must be flagged and reconcilable.
- **Ship:** merge a branch and fix up everything that depended on it.

---

*End of plan. Execute phase by phase. Update the Status checkboxes as you go.*

---

## Appendix A — Event Horizon theme (default)

Default theme is **Event Horizon** (the Omarchy Horizon Dark scheme). Palette pulled from the theme's terminal config. Implement as a Lip Gloss style set; expose every token in `config.yml` under `theme:` so users can override or swap themes.

### Raw palette (source of truth)

| Role (terminal) | Hex |
|---|---|
| background | `#1c1e26` |
| foreground | `#fadad1` |
| black / bright-black | `#1c1e26` / `#6c6f93` |
| red / bright-red | `#e95678` / `#ec6a88` |
| green / bright-green | `#29d398` / `#3fdaa4` |
| yellow / bright-yellow | `#fab795` / `#fbc3a7` |
| blue / bright-blue | `#26bbd9` / `#3fc4de` |
| magenta / bright-magenta | `#ee64ac` / `#f075b5` |
| cyan / bright-cyan | `#59e3e3` / `#6be4e6` |
| white | `#fadad1` |

### Semantic tokens (use these in the UI, not raw hex)

| Token | Hex | Used for |
|---|---|---|
| `base` | `#1c1e26` | app background |
| `surface` | `#232530` | panel background (derived: bg lifted ~one step) |
| `overlay` | `#2e303e` | dividers, **inactive** pane borders (derived) |
| `text` | `#fadad1` | primary text |
| `muted` | `#6c6f93` | secondary text, comments, empty CI dots, inactive nodes |
| `primary` | `#ee64ac` | selection bar, active stack node (the "you are here" stone) |
| `focus` | `#26bbd9` | **focused pane border, section headers, active tab underline, spinners** |
| `info` | `#3fc4de` | PR/issue numbers, links, branch names in the tree |
| `success` | `#29d398` | approved, merged, CI pass dot |
| `warning` | `#fab795` | lineage drift (amber node), CI pending, "attention" |
| `danger` | `#e95678` | CI fail dot, merge conflicts, changes-requested |
| `accent2` | `#59e3e3` | commit hashes, subtle emphasis |

The blue carries the structural chrome: the **focused pane** gets a `focus` (`#26bbd9`) border while unfocused panes use `overlay`, section headers and the active-tab underline are `focus`, and PR numbers/branch names render in the brighter `info` (`#3fc4de`). This makes "where am I" read as blue at a glance, leaving pink (`primary`) purely for the selected row / active stone and coral (`danger`) purely for failure. Three jobs, three unmistakable colors.

Notes: `surface` and `overlay` are derived (Horizon Dark's intermediate shades) since the source config only defines bg + 16 ANSI colors — tune them if panels don't separate cleanly on the user's display. Coral `#e95678` is reserved for `danger` (red = bad is the intuitive read); selection uses magenta-pink `#ee64ac` so focus state never visually collides with error state.

### Sample `config.yml` theme block

```yaml
theme:
  name: event-horizon
  base:     "#1c1e26"
  surface:  "#232530"
  overlay:  "#2e303e"
  text:     "#fadad1"
  muted:    "#6c6f93"
  primary:  "#ee64ac"
  focus:    "#26bbd9"
  info:     "#3fc4de"
  success:  "#29d398"
  warning:  "#fab795"
  danger:   "#e95678"
  accent2:  "#59e3e3"
```

### Stack-tree node rendering (concrete)

The tree is drawn as a cairn — stones stacked bottom (trunk) to top, connected by `│`/`├` guides in `muted`.

- **Active/selected node:** `primary` foreground on a `surface` bar (the highlighted stone).
- **Branch name / PR number:** `info` (blue).
- **CI dot:** `success` (pass) · `danger` (fail) · `warning` (pending/running) · `muted` (none).
- **Drift flag:** node label in `warning` + a small `⚠` glyph.
- **Focused-pane border:** `focus` (blue); unfocused panes use `overlay`.
- **Merged/closed branch (transient, pre-prune):** `muted`, struck-through if the terminal supports it.

