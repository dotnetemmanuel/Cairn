# Installing Cairn

Pick the route that matches your machine. Every one of them installs the same
binary, built by the same release pipeline.

Cairn shells out to `git`, `git-town` and `gh`, and reuses the GitHub CLI's
token. There is no Cairn login and Cairn never stores a token of its own. The
Arch and Homebrew routes pull those three tools in for you; the others tell you
what is missing.

> **Windows is confirmed working as of v0.1.1**, install through the full TUI,
> on Windows 11 in Windows Terminal. It has had far less real-world use than
> Linux and macOS, though, so if something looks wrong, especially box drawing
> in the old console rather than Windows Terminal,
> [say so](https://github.com/dotnetemmanuel/Cairn/issues).

## Arch Linux

The AUR package is built by every release but is not published yet: AUR account
registration was closed when v0.1.0 was cut. Until it goes up, use the one-line
installer below. When it lands, this becomes:

```sh
yay -S cairn-bin        # or: paru -S cairn-bin
```

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

## Windows, with one command

```powershell
irm https://raw.githubusercontent.com/dotnetemmanuel/Cairn/main/scripts/install.ps1 | iex
```

It detects your architecture, downloads the matching build, checks it against
the release's `checksums.txt` before unpacking, installs into
`%LOCALAPPDATA%\Programs\cairn`, and adds that to your user PATH (not the
system one, and it tells you exactly what it changed and how to undo it). Like
Scoop, it cannot install `git-town` or `gh` for you; it prints the `winget`
command for each one missing.

```powershell
# a specific version, or a different directory
& ([scriptblock]::Create((irm .../install.ps1))) -Version v0.1.1
& ([scriptblock]::Create((irm .../install.ps1))) -InstallDir C:\tools\cairn
```

If you would rather read a script before piping it into PowerShell, that is
the right instinct: it is
[scripts/install.ps1](https://github.com/dotnetemmanuel/Cairn/blob/main/scripts/install.ps1),
the Windows counterpart to `install.sh` below.

## macOS and Linux, anything else, with one command

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
`cairn` somewhere on your PATH.

**Which file?** Take **amd64** unless you know otherwise. It covers any Intel or
any AMD processor: AMD designed the instruction set, Intel adopted it, and
Windows calls it x64, so the three names mean one file. A Windows VM on an Intel
or AMD host is amd64 too. **arm64** is only for ARM machines: Apple Silicon Macs
(M1 and later), Snapdragon-based Windows laptops, and ARM Linux boards. Run the
wrong one and Windows says the app cannot run on your PC. `checksums.txt` in the same release covers every
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

On Windows, a binary downloaded through a browser carries a mark Windows uses
to flag unfamiliar apps: double-click `cairn.exe` from Explorer and Microsoft
Defender SmartScreen says "Windows protected your PC", because Cairn is not
signed with a code-signing certificate. Click **More info**, then **Run
anyway**, the same one-time trust decision as the macOS quarantine step above.
`scripts/install.ps1` does not hit this, since SmartScreen only checks files
Explorer launches, not ones a script starts directly.

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
