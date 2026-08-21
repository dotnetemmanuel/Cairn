#!/bin/sh
# Install Cairn from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/dotnetemmanuel/Cairn/main/scripts/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.1.0
#
# Installs into ~/.local/bin (override with CAIRN_INSTALL_DIR). Never installs
# system packages: it checks for the tools Cairn shells out to and tells you what
# to run. Windows is not covered here, use Scoop or the .zip from the release
# page.
set -eu

REPO=dotnetemmanuel/Cairn
INSTALL_DIR=${CAIRN_INSTALL_DIR:-$HOME/.local/bin}
VERSION=

die() { printf 'cairn install: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a tag, e.g. --version v0.1.0"; VERSION=$2; shift 2 ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a directory"; INSTALL_DIR=$2; shift 2 ;;
    -h|--help) say "usage: install.sh [--version vX.Y.Z] [--dir DIR]"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

for tool in curl tar; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required to install Cairn"
done

os=$(uname -s)
case $os in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW*|MSYS*|CYGWIN*) die "on Windows use: scoop install cairn, or the .zip from https://github.com/$REPO/releases" ;;
  *) die "unsupported operating system: $os" ;;
esac

arch=$(uname -m)
case $arch in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $arch. Build from source: https://github.com/$REPO/blob/main/docs/Install.md" ;;
esac

if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$VERSION" ] || die "could not find the latest release. Pass one with --version, or check https://github.com/$REPO/releases"
fi

# Release archives are named without the leading v; see .goreleaser.yaml.
bare=${VERSION#v}
archive="cairn_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading Cairn $VERSION for $os/$arch..."
curl -fsSL -o "$tmp/$archive" "$base/$archive" ||
  die "could not download $archive. Check that $VERSION has a build for $os/$arch: https://github.com/$REPO/releases"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" ||
  die "could not download checksums.txt for $VERSION"

# Verify before unpacking: a truncated or tampered download must never reach the
# PATH.
expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || die "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
else
  die "no sha256sum or shasum found, cannot verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $archive. Do not use this download."

tar -xzf "$tmp/$archive" -C "$tmp"
[ -f "$tmp/cairn" ] || die "the archive did not contain a cairn binary"

mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"
install -m 755 "$tmp/cairn" "$INSTALL_DIR/cairn" 2>/dev/null ||
  { cp "$tmp/cairn" "$INSTALL_DIR/cairn" && chmod 755 "$INSTALL_DIR/cairn"; } ||
  die "could not write to $INSTALL_DIR. Pick another with --dir, or CAIRN_INSTALL_DIR."

say "Installed $("$INSTALL_DIR/cairn" version) to $INSTALL_DIR/cairn"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say ""; say "$INSTALL_DIR is not on your PATH. Add this to your shell profile:"; say "  export PATH=\"\$PATH:$INSTALL_DIR\"" ;;
esac

missing=
missing_pacman=
for tool in git git-town gh; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing="$missing $tool"
    case $tool in
      gh) missing_pacman="$missing_pacman github-cli" ;;
      *) missing_pacman="$missing_pacman $tool" ;;
    esac
  fi
done
if [ -n "$missing" ]; then
  say ""
  say "Cairn shells out to these, and they are not on your PATH:$missing"
  if [ "$os" = darwin ]; then
    say "  brew install$missing"
  elif command -v pacman >/dev/null 2>&1; then
    say "  sudo pacman -S$missing_pacman"
  else
    say "  see https://github.com/$REPO/blob/main/docs/Install.md for your platform"
  fi
fi

say ""
say "Next: gh auth login    (Cairn reuses your gh token, it never stores one)"
say "      cairn doctor     (checks your setup and says what is missing)"
say "      cairn            (launch)"
