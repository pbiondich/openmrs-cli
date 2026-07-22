#!/bin/sh
# omrs installer: fetches the latest release binary for this platform,
# verifies its checksum, and installs it onto the PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/pbiondich/openmrs-cli/main/install.sh | sh
#
# Options via environment:
#   OMRS_VERSION      install a specific version (default: latest release)
#   OMRS_INSTALL_DIR  install directory (default: /usr/local/bin, with
#                     sudo if needed; falls back to ~/.local/bin)
#
# POSIX sh, no bash required. Assumes curl and tar, which macOS and
# essentially every Linux distribution ship by default.
set -eu

REPO="pbiondich/openmrs-cli"
BINARY="omrs"

say()  { printf '%s\n' "$*"; }
fail() { printf 'install failed: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar  >/dev/null 2>&1 || fail "tar is required"

# --- platform detection -------------------------------------------------
OS=$(uname -s)
case "$OS" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  *) fail "unsupported OS: $OS (Windows: download the .zip from https://github.com/$REPO/releases)" ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) fail "unsupported architecture: $ARCH" ;;
esac

# --- resolve version ----------------------------------------------------
if [ "${OMRS_VERSION:-}" = "" ]; then
  TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  [ -n "$TAG" ] || fail "could not determine the latest release (set OMRS_VERSION to pin one)"
else
  TAG="$OMRS_VERSION"
  case "$TAG" in v*|[0-9]*) ;; *) fail "OMRS_VERSION should look like v0.7.2" ;; esac
fi
VERSION=${TAG#v}

ARCHIVE="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$TAG"

say "Installing $BINARY $TAG ($OS/$ARCH)"

# --- download and verify ------------------------------------------------
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL -o "$TMP/$ARCHIVE" "$BASE/$ARCHIVE" \
  || fail "download failed: $BASE/$ARCHIVE"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" \
  || fail "checksum manifest download failed"

WANT=$(grep " $ARCHIVE\$" "$TMP/checksums.txt" | awk '{print $1}')
[ -n "$WANT" ] || fail "no checksum listed for $ARCHIVE"

if command -v sha256sum >/dev/null 2>&1; then
  GOT=$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')
else
  GOT=$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')
fi
[ "$GOT" = "$WANT" ] || fail "checksum mismatch for $ARCHIVE (got $GOT, want $WANT)"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" "$BINARY"
chmod +x "$TMP/$BINARY"

# --- install onto the PATH ----------------------------------------------
install_to() {
  DIR="$1"
  if [ -w "$DIR" ]; then
    mv "$TMP/$BINARY" "$DIR/$BINARY"
  elif [ "$2" = "sudo" ] && command -v sudo >/dev/null 2>&1; then
    say "Installing to $DIR (sudo may prompt for your password)"
    sudo mv "$TMP/$BINARY" "$DIR/$BINARY" || return 1
  else
    return 1
  fi
}

if [ "${OMRS_INSTALL_DIR:-}" != "" ]; then
  mkdir -p "$OMRS_INSTALL_DIR"
  install_to "$OMRS_INSTALL_DIR" nosudo || fail "cannot write to $OMRS_INSTALL_DIR"
  DEST="$OMRS_INSTALL_DIR"
elif install_to /usr/local/bin sudo; then
  DEST=/usr/local/bin
else
  mkdir -p "$HOME/.local/bin"
  install_to "$HOME/.local/bin" nosudo || fail "cannot write to $HOME/.local/bin"
  DEST="$HOME/.local/bin"
fi

say ""
say "Installed: $DEST/$BINARY"

# --- verify and advise ---------------------------------------------------
case ":$PATH:" in
  *":$DEST:"*)
    say "$("$DEST/$BINARY" --version)"
    say ""
    say "Try it:"
    say "  $BINARY login --demo"
    say "  $BINARY whoami"
    ;;
  *)
    say ""
    say "$DEST is not on your PATH. Add it, then open a new terminal:"
    say "  echo 'export PATH=\"\$PATH:$DEST\"' >> ~/.zshrc && source ~/.zshrc"
    ;;
esac
