# Plan: `cairn demo` (fake-data mode for screenshots and first impressions)

> Status: PLANNED. Nothing here is built yet. Branch: `feat/demo-mode`
> (stacked on `docs/readme-rewrite`). Implement in a later session.

## 1. Goal

Ship a first-class demo mode so anyone can run Cairn against a fully synthetic
GitHub, no account, no `gh`, no network. Two payoffs:

1. **Screenshots** for the README with zero real repo/org/user data, deterministic
   and repeatable (author the exact states we want to show).
2. **First impressions**: a new user runs `cairn demo` and explores the whole UI
   before authenticating. Great for a README GIF and for evaluating the tool.

Non-goal: demo mode never mutates anything. Every write path (comment, approve,
merge, propose, ship) is either disabled or a no-op that shows a toast. Demo is
read-only theater.

## 2. Design principle: one thin seam, no refactor

The `internal/gh` package exposes package-level functions (`FetchViewer`,
`SearchItems`, `FetchPRDetail`, ...). The TUI calls them directly. We keep that.

**Approach (chosen): a guard line at the top of each intercepted read function.**
A new file `internal/gh/demo.go` holds a package flag plus all fixtures and demo
variants. Each intercepted function gains one line:

```go
func SearchItems(filter string, limit int) (items []Item, total int, err error) {
    if demoOn { return demoSearch(filter, limit) }
    ...
}
```

- `demoOn` is a package bool set once by `EnableDemo()` (called from `main` for
  the `demo` subcommand, or when `CAIRN_DEMO=1` is in the environment).
- Writes are never intercepted here; they are gated in the TUI (see 6).

**Rejected alternative:** refactor `gh` to a client interface with a real impl
and a demo impl. Cleaner in theory, but it touches every call site across the
TUI and buys us nothing for a read-only demo. The guard line is ~11 one-line
insertions plus one self-contained file, and it keeps the real code paths
obvious. Revisit only if `gh` grows an interface for other reasons.

## 3. Enabling demo mode

Add a `demo` subcommand in `main.go` (alongside `doctor`, `version`, `help`):

```
cairn demo     launch the TUI against built-in fake data (no gh/git-town needed)
```

`runDemo()` mirrors `runTUI()` with two differences:

1. Call `gh.EnableDemo()` before constructing the TUI.
2. **Skip the doctor preflight.** `runTUI()` bails if `doctor.Diagnose()` is not
   OK (missing `gh`/`git-town` or no auth). Demo must run on a clean machine with
   none of that, so `runDemo()` does not call `Diagnose()`. It still loads config
   (theme, sections) so the board looks real; a missing config just uses defaults.

Also honor `CAIRN_DEMO=1` from `runTUI()` itself: if set, flip on demo and skip
preflight. This lets `CAIRN_DEMO=1 cairn` work too, handy for scripted capture.

Add a small visible marker so a demo screenshot is never mistaken for real data
in a support context: a `DEMO` chip in the header statusline (muted, next to the
login). Decide at implementation time whether to keep it in the final README
shots or toggle it off with `CAIRN_DEMO_NOCHIP=1` for the clean captures. Default:
chip ON; capture the README shots with the chip suppressed.

## 4. The fixture cast (all fake, consistent everywhere)

| Role | Value |
|------|-------|
| Viewer login (`@me`) | `avery` |
| Teammates | `jordan`, `kim`, `noor`, `theo` |
| CI/bot actor | `northwind-ci` (not `github-actions`, keep it fully fake) |
| Org | `northwind` |
| Repos | `northwind/atlas` (hosts the stack), `northwind/ledger`, `northwind/beacon` |

Rationale for `northwind`: it is the decades-old Microsoft sample-database company
(Northwind Traders), so it reads instantly as demo data. Siblings if we want more:
`contoso`, `fabrikam`, `acme`. Keep everything under the one org so the Orgs tab
has something coherent to group.

### The stack (in `northwind/atlas`, the centerpiece)

```
main  (trunk)
└─ search-api     PR #141  base main        approved         CI green
   └─ search-index PR #142  base search-api   review required  CI green   ← review/conversation demo PR
      └─ search-ui  PR #143  base search-index draft            CI pending ← drift/amber candidate
```

This one chain drives the hero sidebar (#1), stack mode (#4), the review pane
(#3, on #142), and the conversation view (#7, on #142).

## 5. Function-by-function stub behavior

All demo variants live in `internal/gh/demo.go`. Times are computed relative to a
single fixed base so ordering is stable; do NOT call `time.Now()` at fixture-build
time if we want byte-identical frames (acceptable to use `time.Now()`-relative for
"2h ago" style labels since only the label text matters, not exact pixels).

| Function | Demo behavior |
|----------|---------------|
| `FetchViewer` | `{Login: "avery", RateRemaining: 4987, RateLimit: 5000}` |
| `FetchOrgs` | `["northwind"]` |
| `SearchItems(filter, limit)` | Route by filter substring (see 5a); return the matching `[]Item` set + a plausible `total`. |
| `FetchNotificationFeed(limit)` | A hand-built `[]Notification` with varied reasons/types (see 5b). |
| `FetchNotifications(limit)` | Same data mapped to `[]Item` (used if any non-inbox path calls it); reuse the feed builder. |
| `FetchPRDetail(owner, repo, number)` | Switch on `number`: #142 returns the rich detail (comments, reviews, inline threads, checks, commits, events); other numbers return a lighter detail so opening any board PR still works. |
| `FetchPRFiles(owner, repo, number)` | 2–3 fake `FileDiff` with real unified-diff `Patch` text; #142 includes a file/line that a fixture `ReviewComment` anchors to (so the 💬 badge shows). |
| `OpenPRs(owner, repo)` | The atlas stack as `[]OpenPR` (numbers, heads, bases, mergeability). |
| `OpenPRsByBranch(owner, repo)` | `map[branch]PRMergeability` for the stack branches. |
| `OpenPRNumbersByBranch(owner, repo)` | `{search-api:141, search-index:142, search-ui:143}`. |
| `LandedPRsByBranch(owner, repo, branches)` | Empty (nothing landed) for the clean shot; optionally mark one landed to demo the reconcile affordance. |
| `PRsWithBase(owner, repo, base)` | Child PR numbers for a given base, consistent with the stack. |

### 5a. SearchItems routing

The board runs one filter per tab (and a `closedFilter` variant for the closed
tail). Route on substrings; finalize exact markers by reading how the sections
build filters (`config` defaults + `SectionInvolved`/`SectionOrgs` query builders
in `internal/tui/app.go`):

- contains `is:closed` or `is:merged` -> the **closed tail** set (2–3 muted rows).
- contains `author:@me` -> **My PRs** (the three atlas stack PRs + one in ledger).
- contains `review-requested:@me` -> **Needs my review** (PRs by jordan/kim in
  atlas/beacon).
- contains `org:northwind` -> **Orgs** group set (org-wide mix).
- involved sub-queries (assigned / mentioned / review-requested / commented) ->
  small per-group sets.
- default/fallback -> a generic mixed set, so an unrecognized filter still renders.

Encode `HeadBranch`/`BaseBranch` on the atlas items so the sidebar reconstructs
the stack from base/head chains without any extra calls.

### 5b. Notifications feed

6–8 entries spanning the glyph/reason space so the inbox looks alive:
`review_requested` (PR), `mention` (PR), `comment` (Issue), `ci_activity`
(CheckSuite), `author` (PR), `subscribed` (Release). Mix `Unread: true/false` so
the UNREAD/READ split shows. One entry should be `northwind/atlas #142` so the
preview pane can render a thread we also define in `FetchPRDetail`.

## 6. Write paths in demo mode

Demo is read-only. In the TUI, the action dispatchers (comment, approve,
request-changes, reply, merge, ship, propose, ready, mark-read) should short-
circuit when `gh.DemoActive()` is true and show a toast like
`demo mode: writes are disabled`. Cheapest correct approach: a single
`gh.DemoActive()` check in the command layer where these `tea.Cmd`s are built.
Enumerate the call sites during implementation (they are the `do*`/`run*`
helpers in `detail.go`, `app.go`, `stackmode.go`). Keep it to one guard per
dispatcher.

git-town/`git` shell-outs (stack mutations, conflict continue) are NOT reachable
through demo mode data, but stack mode itself runs against the real cwd repo. For
the screenshots we drive those from a throwaway repo (see 7), so we do not need
to stub git-town. If we later want `cairn demo` to fake stack mutations too,
that is a follow-up; out of scope here.

## 7. The two git-backed screens (stack mode #4, conflict #5)

These read real git config and run real `git-town`/`git` on the current
directory, so fixtures in `gh` do not reach them. Capture them from a scripted
throwaway repo, with fake branch names that match the fixture stack so the demo
gh overlay (PR numbers/CI) lines up.

Runbook (a script we add under `docs/` or `scripts/`, e.g. `scripts/demo-repo.sh`):

1. `mkdir -p /tmp/northwind-atlas && cd /tmp/northwind-atlas && git init -b main`
2. Seed a few files, commit on `main` (the trunk).
3. `git config git-town.main-branch main`.
4. Build the stack with real branches + lineage config:
   `search-api` -> `search-index` -> `search-ui`, each with a commit, and
   `git config git-town-branch.<child>.parent <parent>` for each (same keys Cairn
   reads).
5. For the **amber drift** in the shot: amend a commit on `search-api` WITHOUT
   restacking its descendants, so `search-index`'s recorded parent no longer
   matches its actual merge-base. Cairn flags it amber with the ⚠.
6. Launch `CAIRN_DEMO=1 cairn` from `/tmp/northwind-atlas`, press `S` -> stack
   mode. The tree is real (fake names), the PR-number/CI overlay comes from the
   demo gh stubs keyed to those branch names. Capture #4.
7. For **conflict resolver #5**: make `main` and `search-api` edit the same line,
   then trigger a `restack`/`sync` so it conflicts; the resolver opens. Capture
   #5. (This runs real git; demo gh is irrelevant here.)

Keep the script idempotent (nuke and recreate `/tmp/northwind-atlas` each run) so
captures are repeatable.

## 8. File layout

```
internal/gh/demo.go        // demoOn flag, EnableDemo/DemoActive, all fixtures + demo* funcs
internal/gh/demo_test.go   // fixtures are self-consistent (numbers match branches, anchors exist)
main.go                    // `demo` subcommand + CAIRN_DEMO env handling + preflight skip
internal/tui/statusline.go // optional DEMO chip in the header
scripts/demo-repo.sh       // throwaway repo seeder for #4 and #5
docs/Install.md            // add a "Try it with no account: cairn demo" note
README.md                  // (screenshots land on the docs/readme-screenshots branch above this one)
```

Guard-line insertions (one each): `client.go` (FetchViewer, FetchOrgs),
`search.go` (SearchItems), `notifications.go` (FetchNotificationFeed,
FetchNotifications), `pr.go` (FetchPRDetail, FetchPRFiles, OpenPRs,
OpenPRsByBranch, OpenPRNumbersByBranch, LandedPRsByBranch, PRsWithBase).

## 9. Testing

- `demo_test.go`: assert internal consistency so a fixture edit cannot silently
  break a screen:
  - every `OpenPRNumbersByBranch` branch appears in the stack and vice versa;
  - `FetchPRDetail(#142)` has at least one `ReviewComment` whose `Path`/`Line`
    exists in `FetchPRFiles(#142)` (so the 💬 badge always has an anchor);
  - `SearchItems(author:@me)` items all have non-empty `HeadBranch`/`BaseBranch`
    so the sidebar can reconstruct the stack;
  - the notification feed references at least one PR that `FetchPRDetail` knows.
- `EnableDemo()` then a call to each intercepted function returns non-empty,
  error-free data.
- Existing tests stay green (guard lines are inert when `demoOn` is false).
- `go build ./... && go test ./...` and a manual `cairn demo` smoke per the
  capture runbook.

## 10. Screenshot capture runbook (fixtures -> the 7 README shots)

| # | Screen | How to capture |
|---|--------|----------------|
| 1 | Hero | `cairn demo`, select an atlas stack PR so the sidebar renders; wide terminal (>=120 cols). |
| 2 | Board | `cairn demo`, Orgs or Needs-my-review tab, sidebar off (`s`), OPEN/CLOSED divider visible. |
| 3 | Review (diff) | Open #142, cursor on the commented line so the info pane shows the 💬 line thread. |
| 4 | Stack mode | From `/tmp/northwind-atlas` (script), `CAIRN_DEMO=1 cairn`, `S`; amber drift present. |
| 5 | Conflict | From the same repo with a seeded conflict, trigger restack; resolver opens. |
| 6 | Notifications | `cairn demo`, Notifications tab, focus the preview (`->`/enter). |
| 7 | Conversation | Open #142, press `v` for the full threaded timeline. |

Shots 1,2,3,6,7 come from `cairn demo` alone. Shots 4,5 come from the throwaway
repo (still `CAIRN_DEMO=1` for the overlay on #4).

## 11. Open decisions (resolve at implementation)

- **DEMO chip in the final README shots:** keep for honesty, or suppress for a
  cleaner look? Leaning: suppress in README, keep by default in the app.
- **Show a landed/closed branch** in the stack to demo the reconcile (`X`)
  affordance, or keep the stack pristine? Could do a second variant set.
- **Exact filter markers** for SearchItems routing: confirm against the real
  section query builders before hardcoding substrings.
- Where the seeder script lives (`scripts/` vs `docs/`) and whether it ships.

## 12. Task checklist (execution order)

1. `internal/gh/demo.go`: `demoOn`, `EnableDemo()`, `DemoActive()`, the cast
   constants, and the fixture builders (viewer, orgs, items-by-tab, notifications,
   pr-detail, pr-files, stack maps).
2. Guard lines in `client.go`, `search.go`, `notifications.go`, `pr.go`.
3. `main.go`: `demo` subcommand + `CAIRN_DEMO` env + preflight skip; `help` text.
4. Write-path guards in the TUI dispatchers (toast on demo).
5. Optional DEMO chip in the statusline.
6. `demo_test.go` consistency tests; keep the suite green.
7. `scripts/demo-repo.sh` seeder for #4/#5.
8. `docs/Install.md`: add the `cairn demo` note.
9. Manual capture pass -> save PNGs (that work happens on the
   `docs/readme-screenshots` branch stacked above this one).
```
