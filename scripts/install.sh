#!/bin/sh
# install.sh — one-shot installer for the mio CLI
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Searchie-Inc/mio-cli/main/scripts/install.sh | sh
#
# Environment overrides:
#   VERSION   — install a specific tag, e.g. VERSION=1.2.3 ./install.sh
#   PREFIX    — install directory, defaults to /usr/local/bin
#
# POSIX-compliant; no bash required.

set -eu

REPO="Searchie-Inc/mio-cli"
BINARY="mio"
PREFIX="${PREFIX:-/usr/local/bin}"

# ── helpers ──────────────────────────────────────────────────────────────────

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
info() { printf '  \033[32m%s\033[0m %s\n' "=>" "$*"; }

# ── detect OS ─────────────────────────────────────────────────────────────────

detect_os() {
  case "$(uname -s)" in
    Darwin)  echo "darwin"  ;;
    Linux)   echo "linux"   ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
             echo "windows" ;;
    *)       die "unsupported operating system: $(uname -s)" ;;
  esac
}

# ── detect arch ───────────────────────────────────────────────────────────────

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)    echo "amd64"  ;;
    aarch64|arm64)   echo "arm64"  ;;
    armv7l|armv6l)   die "32-bit ARM is not supported. Please build from source." ;;
    *)               die "unsupported architecture: $(uname -m)" ;;
  esac
}

# ── resolve latest version from GitHub API ────────────────────────────────────

latest_version() {
  url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" \
      | grep '"tag_name"' \
      | sed 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/'
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$url" \
      | grep '"tag_name"' \
      | sed 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/'
  else
    die "curl or wget is required to download releases"
  fi
}

# ── download helper ───────────────────────────────────────────────────────────

download() {
  url="$1"
  dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    die "curl or wget is required"
  fi
}

# ── main ──────────────────────────────────────────────────────────────────────

main() {
  OS="$(detect_os)"
  ARCH="$(detect_arch)"

  # Resolve version
  if [ -z "${VERSION:-}" ]; then
    info "Fetching latest release version..."
    VERSION="$(latest_version)"
    [ -n "$VERSION" ] || die "could not determine latest release version"
  fi

  info "Installing mio v${VERSION} (${OS}/${ARCH}) → ${PREFIX}/${BINARY}"

  # Build archive name matching goreleaser template:
  # mio_<version>_<os>_<arch>.tar.gz  (zip on Windows)
  ARCHIVE_BASE="${BINARY}_${VERSION}_${OS}_${ARCH}"
  case "$OS" in
    windows) EXT="zip"    ;;
    *)       EXT="tar.gz" ;;
  esac
  ARCHIVE="${ARCHIVE_BASE}.${EXT}"

  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${ARCHIVE}"
  CHECKSUM_URL="https://github.com/${REPO}/releases/download/v${VERSION}/checksums.txt"

  # Work in a temporary directory
  TMP_DIR="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '${TMP_DIR}'" EXIT

  info "Downloading ${ARCHIVE}..."
  download "$DOWNLOAD_URL" "${TMP_DIR}/${ARCHIVE}"

  # Verify checksum if shasum/sha256sum is available
  if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
    info "Verifying checksum..."
    download "$CHECKSUM_URL" "${TMP_DIR}/checksums.txt"

    EXPECTED="$(grep " ${ARCHIVE}$" "${TMP_DIR}/checksums.txt" | awk '{print $1}')"
    if [ -n "$EXPECTED" ]; then
      if command -v sha256sum >/dev/null 2>&1; then
        ACTUAL="$(sha256sum "${TMP_DIR}/${ARCHIVE}" | awk '{print $1}')"
      else
        ACTUAL="$(shasum -a 256 "${TMP_DIR}/${ARCHIVE}" | awk '{print $1}')"
      fi
      if [ "$ACTUAL" != "$EXPECTED" ]; then
        die "checksum mismatch — expected ${EXPECTED}, got ${ACTUAL}"
      fi
      info "Checksum verified."
    else
      info "No checksum entry found for ${ARCHIVE}; skipping verification."
    fi
  fi

  # Extract binary
  info "Extracting..."
  case "$EXT" in
    tar.gz)
      need tar
      tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "${TMP_DIR}" "${BINARY}"
      ;;
    zip)
      need unzip
      unzip -q "${TMP_DIR}/${ARCHIVE}" "${BINARY}.exe" -d "${TMP_DIR}" || \
      unzip -q "${TMP_DIR}/${ARCHIVE}" "${BINARY}"       -d "${TMP_DIR}"
      ;;
  esac

  # Determine the extracted binary name (Windows adds .exe)
  if [ -f "${TMP_DIR}/${BINARY}.exe" ]; then
    SRC="${TMP_DIR}/${BINARY}.exe"
    DEST_NAME="${BINARY}.exe"
  else
    SRC="${TMP_DIR}/${BINARY}"
    DEST_NAME="${BINARY}"
  fi

  # Install
  DEST="${PREFIX}/${DEST_NAME}"

  SUDO=""
  if [ ! -w "$PREFIX" ]; then
    info "Destination ${PREFIX} requires elevated permissions — running sudo..."
    SUDO="sudo"
  fi
  # Stage into the destination DIRECTORY and rename — never cp onto $DEST.
  #
  # On Linux, opening a file for writing while it is being EXECUTED fails with
  # ETXTBSY ("Text file busy"), and `mio update` re-runs this installer from the
  # very binary it is replacing — so `cp "$SRC" "$DEST"` fails for every
  # self-update, while a fresh install (nothing running) succeeds. That is why
  # this only ever bit the update path.
  #
  # rename(2) swaps the directory entry instead: the running process keeps its
  # old inode and the next exec picks up the new one. Staging inside $PREFIX
  # keeps it a true rename — a cross-filesystem `mv` degrades to copy-then-
  # unlink, which would hit ETXTBSY all over again. chmod happens BEFORE the
  # rename so $DEST is never briefly present and non-executable.
  STAGED="${DEST}.new.$$"
  # shellcheck disable=SC2064 # $STAGED/$SUDO are intentionally expanded now
  trap "rm -rf '${TMP_DIR}'; ${SUDO} rm -f '${STAGED}'" EXIT
  $SUDO cp "$SRC" "$STAGED"
  $SUDO chmod +x "$STAGED"
  $SUDO mv -f "$STAGED" "$DEST"
  # shellcheck disable=SC2064
  trap "rm -rf '${TMP_DIR}'" EXIT

  # macOS Gatekeeper mitigation (MIO-2603): the release binaries are not yet
  # notarized, so a freshly downloaded copy trips Gatekeeper — `mio version` can
  # hang on the syspolicy check and `spctl --assess` reports "rejected". As a
  # best-effort local mitigation, drop the com.apple.quarantine attribute and
  # ad-hoc codesign the binary so the CLI runs immediately. Both are best-effort
  # (never fail the install); this is a stopgap, not a substitute for notarizing
  # the release in the pipeline.
  if [ "$OS" = "darwin" ]; then
    $SUDO xattr -d com.apple.quarantine "$DEST" 2>/dev/null || true
    if command -v codesign >/dev/null 2>&1; then
      $SUDO codesign --force --sign - "$DEST" >/dev/null 2>&1 || true
    fi
    info "macOS: cleared quarantine + ad-hoc signed the binary. If mio ever hangs after an update, run: xattr -c \"$DEST\""
  fi

  # Report the path we actually wrote. `command -v` resolves against PATH, which
  # may be a DIFFERENT binary entirely (an older copy earlier in PATH, or the
  # system one when installing to a custom PREFIX) — it printed
  # "/usr/local/bin/mio" during a --prefix install to a temp dir, which is a lie
  # about what just happened.
  info "Installed: ${DEST}"
  RESOLVED="$(command -v "${BINARY}" 2>/dev/null || true)"
  if [ -n "$RESOLVED" ] && [ "$RESOLVED" != "$DEST" ]; then
    info "Note: '${BINARY}' on your PATH still resolves to ${RESOLVED} — put ${PREFIX} earlier in PATH to pick up this install."
  fi
  printf '\n'
  printf '  \033[1mNext steps:\033[0m\n'
  printf '    1. Run \033[36mmio login\033[0m to authenticate with your Membership.io account.\n'
  printf '    2. Run \033[36mmio --help\033[0m to explore available commands.\n'
  printf '\n'
  printf '  Docs: https://docs.membership.io/cli\n'
  printf '\n'
}

main "$@"
