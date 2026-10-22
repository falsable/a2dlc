#!/usr/bin/env sh
# install.sh — One-line installer for a2dlc (Linux, macOS, WSL)
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/falsable/a2dlc/main/install.sh | sh
set -eu

REPO="falsable/a2dlc"
VERSION="${A2DLC_VERSION:-v0.3.0}"
INSTALL_DIR="${A2DLC_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  linux*)  OS="linux" ;;
  darwin*) OS="darwin" ;;
  *)
    echo "Error: Unsupported OS '$OS'. On Windows PowerShell, use install.ps1." >&2
    exit 1
    ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Error: Unsupported architecture '$ARCH'." >&2
    exit 1
    ;;
esac

BIN_NAME="a2dlc-${OS}-${ARCH}"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${BIN_NAME} (${VERSION}) from ${REPO}..."
curl -fsSL "${BASE_URL}/${BIN_NAME}" -o "${TMP_DIR}/${BIN_NAME}"
curl -fsSL "${BASE_URL}/SHA256SUMS" -o "${TMP_DIR}/SHA256SUMS"

echo "Verifying SHA-256 checksum..."
(
  cd "$TMP_DIR"
  EXPECTED="$(grep " ${BIN_NAME}\$" SHA256SUMS | awk '{print $1}')"
  if [ -z "$EXPECTED" ]; then
    echo "Error: ${BIN_NAME} not found in SHA256SUMS" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "$BIN_NAME" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL="$(shasum -a 256 "$BIN_NAME" | awk '{print $1}')"
  else
    echo "Error: Neither sha256sum nor shasum found to verify binary." >&2
    exit 1
  fi
  if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Error: SHA-256 mismatch! Expected $EXPECTED, got $ACTUAL" >&2
    exit 1
  fi
)

mkdir -p "$INSTALL_DIR"
mv "${TMP_DIR}/${BIN_NAME}" "${INSTALL_DIR}/a2dlc"
chmod +x "${INSTALL_DIR}/a2dlc"

echo "Installed a2dlc to ${INSTALL_DIR}/a2dlc"
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo "Note: Add ${INSTALL_DIR} to your PATH:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

if [ -d ".git" ]; then
  mkdir -p .claude/skills/a2dlc .agents/skills/a2dlc
  if curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/skills/a2dlc/SKILL.md" -o .claude/skills/a2dlc/SKILL.md 2>/dev/null; then
    cp .claude/skills/a2dlc/SKILL.md .agents/skills/a2dlc/SKILL.md
    echo "Installed agent skill to .claude/skills/a2dlc/SKILL.md and .agents/skills/a2dlc/SKILL.md"
  fi
fi

echo "Done! Run 'a2dlc install --agent claude --agent gemini' inside your repository."
