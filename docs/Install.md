# Installing Cairn

> **Note:** Cairn is pre-packaging. This guide covers building from source.
> Once the distribution phase lands, this page will document `yay -S cairn-bin`
> (AUR) and downloadable release binaries. For now, source is the way.

## Prerequisites

Cairn is a single Go binary that shells out to three tools and reuses your
existing `gh` login. It never stores a token of its own.

| Tool | Why it's needed | Install (Arch) |
|------|-----------------|----------------|
| [Go](https://go.dev) ≥ 1.26 | builds the binary | `pacman -S go` |
| [git](https://git-scm.com) | diffs, status, and command fall-through | `pacman -S git` |
| [git-town](https://www.git-town.com) | every stack mutation (sync / ship / restack) | `pacman -S git-town` |
| [gh](https://cli.github.com) | provides the auth token Cairn reads | `pacman -S github-cli` |

On non-Arch systems, install the equivalents from each project's website.

## Build from source

Go resolves all library dependencies automatically from `go.mod` / `go.sum`, so
there's no separate dependency step:

```sh
git clone https://github.com/dotnetemmanuel/Cairn.git
cd Cairn
go build -o cairn .        # produces ./cairn in the repo root
```

That's the whole build. No code generation, no extra steps. The first build
downloads the Go module cache; later builds reuse it.

## Put it on your PATH (optional)

```sh
# Copy the built binary somewhere on your PATH:
cp ./cairn ~/.local/bin/cairn

# ...or symlink it during development so rebuilds are picked up automatically:
ln -sf "$PWD/cairn" ~/.local/bin/cairn
```

Then `cairn` works from any directory (assuming `~/.local/bin` is on your PATH).

## Authenticate

Cairn reads the GitHub CLI's token. There is no separate Cairn login. Once:

```sh
gh auth login
gh auth refresh -s read:org,workflow   # org repos need read:org; Actions need workflow
```

## Verify

```sh
cairn doctor     # checks git, git-town, gh, and auth; tells you what's missing
```

When `doctor` is green, launch it:

```sh
cairn            # launch the TUI
cairn version    # print version
cairn help       # usage
```

Any unrecognized subcommand falls through to `git`, so `cairn status` behaves
exactly like `git status`.
