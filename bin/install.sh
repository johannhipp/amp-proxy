#!/usr/bin/env bash
set -euo pipefail

# amp-proxy installer
# Builds amp-proxy from source, downloads cli-proxy-api-plus for auth,
# and installs both to ~/.local/bin.

INSTALL_DIR="${AMP_PROXY_INSTALL_DIR:-$HOME/.local/bin}"
CLIPROXY_VERSION="${CLIPROXY_VERSION:-latest}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
RESET='\033[0m'

info()  { echo -e "${BLUE}==>${RESET} ${BOLD}$*${RESET}"; }
ok()    { echo -e "${GREEN}  ✓${RESET} $*"; }
warn()  { echo -e "${YELLOW}  !${RESET} $*"; }
fail()  { echo -e "${RED}  ✗${RESET} $*"; exit 1; }

# --- Detect platform ---

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  darwin) ;;
  linux)  ;;
  *)      fail "Unsupported OS: $OS (macOS and Linux only)" ;;
esac

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)       fail "Unsupported architecture: $ARCH" ;;
esac

info "Platform: ${OS}/${ARCH}"

# --- Check Go ---

if ! command -v go &>/dev/null; then
  fail "Go is required but not found. Install it: https://go.dev/dl/"
fi

GO_VERSION="$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1)"
info "Go: ${GO_VERSION}"

# --- Build amp-proxy ---

info "Building amp-proxy..."

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ ! -f "$REPO_DIR/go.mod" ]; then
  fail "Run this script from the amp-proxy repo (expected go.mod at $REPO_DIR/go.mod)"
fi

VERSION="$(cd "$REPO_DIR" && git describe --tags --always --dirty 2>/dev/null || echo 'dev')"

(cd "$REPO_DIR" && go build -ldflags "-s -w -X main.version=${VERSION}" -o "$REPO_DIR/bin/amp-proxy" .)
ok "Built amp-proxy ${VERSION}"

# --- Download cli-proxy-api-plus ---

info "Downloading cli-proxy-api-plus (needed for 'amp-proxy login')..."

if [ "$CLIPROXY_VERSION" = "latest" ]; then
  CLIPROXY_TAG="$(curl -sL https://api.github.com/repos/router-for-me/CLIProxyAPIPlus/releases/latest | grep '"tag_name"' | head -1 | cut -d'"' -f4)"
else
  CLIPROXY_TAG="$CLIPROXY_VERSION"
fi

if [ -z "$CLIPROXY_TAG" ]; then
  fail "Could not determine CLIProxyAPIPlus release version"
fi

# Tag format: v6.9.4-2 → version 6.9.4-2
CLIPROXY_VER="${CLIPROXY_TAG#v}"
TARBALL="CLIProxyAPIPlus_${CLIPROXY_VER}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/router-for-me/CLIProxyAPIPlus/releases/download/${CLIPROXY_TAG}/${TARBALL}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

if ! curl -sL --fail -o "$TMPDIR/$TARBALL" "$DOWNLOAD_URL"; then
  fail "Download failed: $DOWNLOAD_URL"
fi

tar -xzf "$TMPDIR/$TARBALL" -C "$TMPDIR" cli-proxy-api-plus
ok "Downloaded cli-proxy-api-plus ${CLIPROXY_TAG}"

# --- Install ---

info "Installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"

cp "$REPO_DIR/bin/amp-proxy" "$INSTALL_DIR/amp-proxy"
chmod +x "$INSTALL_DIR/amp-proxy"
ok "Installed amp-proxy"

cp "$TMPDIR/cli-proxy-api-plus" "$INSTALL_DIR/cli-proxy-api-plus"
chmod +x "$INSTALL_DIR/cli-proxy-api-plus"
ok "Installed cli-proxy-api-plus"

# --- Check PATH ---

if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  warn "${INSTALL_DIR} is not in your PATH"
  echo ""
  echo "  Add it to your shell profile:"
  echo ""
  if [ -f "$HOME/.zshrc" ]; then
    echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc && source ~/.zshrc"
  else
    echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc && source ~/.bashrc"
  fi
  echo ""
fi

# --- Done ---

echo ""
echo -e "${GREEN}${BOLD}Installation complete!${RESET}"
echo ""
echo "  Next steps:"
echo ""
echo "    1. Authenticate with your provider(s):"
echo ""
echo -e "       ${BOLD}amp-proxy login claude${RESET}     # Claude Max / Pro"
echo -e "       ${BOLD}amp-proxy login openai${RESET}     # ChatGPT Plus / Pro"
echo ""
echo "    2. Start the proxy:"
echo ""
echo -e "       ${BOLD}amp-proxy${RESET}"
echo ""
echo "    3. Point Amp at the proxy:"
echo ""
echo -e "       ${BOLD}echo '{\"amp.url\": \"http://localhost:18317\"}' > ~/.config/amp/settings.json${RESET}"
echo ""
echo "    Optional: enable web search (get a key at https://exa.ai):"
echo ""
echo -e "       ${BOLD}EXA_API_KEY=your-key amp-proxy${RESET}"
echo ""
echo "  Check auth status anytime:"
echo ""
echo -e "       ${BOLD}amp-proxy status${RESET}"
echo ""
