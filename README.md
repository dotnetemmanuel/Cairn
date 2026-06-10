# Cairn

> A terminal cockpit for GitHub: dashboard + review + stacked-PR workflow in one TUI.

A cairn is a stack of balanced stones used as a trail marker — a *stack*, a
*waypoint you navigate by*, and stone-balancing as a *mindful* practice. Cairn
brings the PR board, in-terminal review, and stacked-PR authoring into one
keyboard-driven cockpit that runs fully local.

See [`cairn-build-plan.md`](./cairn-build-plan.md) for the full design and the
phased roadmap.

> **Status:** Phase 0 (scaffolding) complete — authenticated header + `doctor`.
> The dashboard, review pane, and stack tree land in later phases.

## Prerequisites

Cairn shells out to three tools and reuses your existing `gh` login (it never
stores a token of its own):

| Tool | Why | Install (Arch) |
|---|---|---|
| [Go](https://go.dev) ≥ 1.26 | builds the binary | `pacman -S go` |
| [git](https://git-scm.com) | diff/status + command fall-through | `pacman -S git` |
| [git-town](https://www.git-town.com) | all stack mutations (sync/ship/restack) | `pacman -S git-town` |
| [gh](https://cli.github.com) | provides the auth token Cairn reads | `pacman -S github-cli` |

On other systems, install the equivalents from each project's site.

## Build from source

Anyone cloning the repo builds the binary the same way — Go resolves all
library dependencies automatically from `go.mod`/`go.sum`:

```sh
git clone https://github.com/dotnetemmanuel/Cairn.git
cd Cairn
go build -o cairn .        # produces ./cairn in the repo root
```

That's the whole build. No code generation, no extra steps. The first build
downloads the Go module dependencies (cached afterwards).

### Install onto your PATH (optional)

```sh
# Copy the built binary somewhere on your PATH:
cp ./cairn ~/.local/bin/cairn

# …or symlink it during development so rebuilds are picked up automatically:
ln -sf "$PWD/cairn" ~/.local/bin/cairn
```

Then `cairn` works from any directory (assuming `~/.local/bin` is on your PATH).

## Authenticate

Cairn uses the GitHub CLI's token — no separate login. Once:

```sh
gh auth login
gh auth refresh -s read:org,workflow   # org repos need read:org; Actions need workflow
```

## Run

```sh
cairn            # launch the TUI
cairn doctor     # diagnose tools (git, git-town, gh) + auth
cairn version    # print version
cairn help       # usage
```

Any unrecognized subcommand falls through to `git` — so `cairn status` behaves
exactly like `git status`, and your muscle memory is never blocked.

## Configuration

Optional, at `~/.config/cairn/config.yml`. Cairn runs on built-in defaults (the
**Event Horizon** theme, trunk `main`) if the file is absent. Theme tokens and
more are documented in the build plan, Appendix A.

## License

MIT.
