# Installing Cairn

Pick the route that matches your machine. Every one of them installs the same
binary, built by the same release pipeline.

Cairn shells out to `git`, `git-town` and `gh`, and reuses the GitHub CLI's
token. There is no Cairn login and Cairn never stores a token of its own. The
Arch and Homebrew routes pull those three tools in for you; the others tell you
what is missing.

> **Windows is experimental in v0.1.0.** The binaries build cleanly and nothing
> in the code is Unix-only, but nobody has run Cairn on Windows yet. Expect the
> box drawing to look right in Windows Terminal and poor in the old console. If
> you try it, please [say how it went](https://github.com/dotnetemmanuel/Cairn/issues).

## Arch Linux

```sh
yay -S cairn-bin        # or: paru -S cairn-bin
```

Updates arrive with your normal system upgrade.

## macOS and Linux, with Homebrew

```sh
brew install dotnetemmanuel/tap/cairn
```

## Windows, with Scoop

```powershell
scoop bucket add cairn https://github.com/dotnetemmanuel/scoop-bucket
scoop install cairn
```

Scoop cannot install `git-town` or `gh` for you. Get them from
[git-town.com](https://www.git-town.com) and [cli.github.com](https://cli.github.com),
or `scoop install git gh` plus the git-town release binary.

## Anything else, with one command

```sh
curl -fsSL https://raw.githubusercontent.com/dotnetemmanuel/Cairn/main/scripts/install.sh | sh
```

It detects your platform, downloads the matching build, checks it against the
release's `checksums.txt` before unpacking, and installs into `~/.local/bin`.

```sh
# a specific version, or a different directory
curl -fsSL .../install.sh | sh -s -- --version v0.1.0
curl -fsSL .../install.sh | sh -s -- --dir /usr/local/bin
```

If you would rather read a script before piping it into a shell, that is the
right instinct: it is
[scripts/install.sh](https://github.com/dotnetemmanuel/Cairn/blob/main/scripts/install.sh),
around a hundred lines.

## Straight from the releases page

Download the archive for your platform from
[Releases](https://github.com/dotnetemmanuel/Cairn/releases), unpack it, and put
`cairn` somewhere on your PATH. `checksums.txt` in the same release covers every
archive:

```sh
sha256sum -c checksums.txt --ignore-missing
```

On macOS, a binary downloaded this way is quarantined and Gatekeeper will refuse
to run it, because Cairn is not signed with a paid Apple certificate. Clear it
once:

```sh
xattr -d com.apple.quarantine ./cairn
```

The Homebrew route avoids that.

## Building from source

You need [Go](https://go.dev) 1.26 or newer. Go resolves the library
dependencies from `go.mod` itself, so there is no separate dependency step.

```sh
git clone https://github.com/dotnetemmanuel/Cairn.git
cd Cairn
go build -o cairn .
```

A source build reports its version as `dev`, which is how you can tell it apart
from a release. Put it on your PATH:

```sh
cp ./cairn ~/.local/bin/cairn
# or, while developing, symlink it so rebuilds are picked up:
ln -sf "$PWD/cairn" ~/.local/bin/cairn
```

## The tools Cairn needs

| Tool | Why | Arch | macOS |
|------|-----|------|-------|
| [git](https://git-scm.com) | diffs, status, command fall-through | `pacman -S git` | preinstalled |
| [git-town](https://www.git-town.com) | every stack mutation (sync, ship, restack) | `pacman -S git-town` | `brew install git-town` |
| [gh](https://cli.github.com) | provides the token Cairn reads | `pacman -S github-cli` | `brew install gh` |

## Authenticate, then check

```sh
gh auth login
gh auth refresh -s read:org,workflow   # org repos need read:org, Actions need workflow
cairn doctor                           # says what is missing, in plain language
cairn                                  # launch
```

Any unrecognized subcommand falls through to `git`, so `cairn status` behaves
exactly like `git status`.

Something wrong? [Open an issue](https://github.com/dotnetemmanuel/Cairn/issues).
The bug form asks for `cairn version` and `cairn doctor` output, which is
usually enough to reproduce it.
