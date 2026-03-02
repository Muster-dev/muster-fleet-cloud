#!/usr/bin/env bash
# Muster Fleet Cloud installer — curl | bash compatible, bash 3.2+
# Usage: curl -fsSL https://raw.githubusercontent.com/Muster-dev/muster-fleet-cloud/main/install.sh | bash
#   or:  curl -fsSL ... | bash -s -- --agent --version 0.2.0 --prefix /usr/local/bin
set -euo pipefail

REPO="Muster-dev/muster-fleet-cloud"
BASE_URL="https://github.com/${REPO}/releases/download"
die() { printf '%b\n' "Error: $1" >&2; exit 1; }
info() { printf '%b\n' "$1"; }

VERSION=""; PREFIX=""; INSTALL_AGENT=0; INSTALL_TUNNEL=0
while [ $# -gt 0 ]; do
  case "$1" in
    --agent)   INSTALL_AGENT=1; shift ;;
    --tunnel)  INSTALL_TUNNEL=1; shift ;;
    --all)     INSTALL_AGENT=1; INSTALL_TUNNEL=1; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    --prefix)  PREFIX="$2"; shift 2 ;;
    -h|--help)
      info "Usage: install.sh [--agent] [--tunnel] [--all] [--version VER] [--prefix DIR]"
      info ""; info "  --agent     Install muster-agent only"
      info "  --tunnel    Install muster-tunnel only"
      info "  --all       Install agent + tunnel (default)"
      info "  --version   Pin to a specific version (default: latest)"
      info "  --prefix    Install directory (default: ~/.muster/bin)"; exit 0 ;;
    *) die "Unknown flag: $1" ;;
  esac
done
if [ "$INSTALL_AGENT" -eq 0 ] && [ "$INSTALL_TUNNEL" -eq 0 ]; then
  INSTALL_AGENT=1; INSTALL_TUNNEL=1
fi
PREFIX="${PREFIX:-${HOME}/.muster/bin}"

# --- detect platform -------------------------------------------------------
case "$(uname -s)" in
  Linux*)  OS="linux" ;;
  Darwin*) OS="darwin" ;;
  *)       die "Unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64)       ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)            die "Unsupported architecture: $(uname -m)" ;;
esac

# --- resolve version -------------------------------------------------------
if [ -z "$VERSION" ]; then
  info "Fetching latest release..."
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"v\([^"]*\)".*/\1/')"
  [ -z "$VERSION" ] && die "Could not determine latest version. Use --version to pin one."
fi
info "Installing muster-fleet-cloud v${VERSION} (${OS}/${ARCH})"

# --- download + install ----------------------------------------------------
mkdir -p "$PREFIX"

download_binary() {
  _name="$1"; _url="${BASE_URL}/v${VERSION}/${_name}-${OS}-${ARCH}"; _dest="${PREFIX}/${_name}"
  info "  Downloading ${_name}..."
  curl -fsSL -o "$_dest" "$_url" || die "Download failed: $_url"
  chmod 755 "$_dest"
  info "  Installed ${_dest}"
}

if [ "$INSTALL_AGENT" -eq 1 ]; then download_binary "muster-agent"; fi
if [ "$INSTALL_TUNNEL" -eq 1 ]; then download_binary "muster-tunnel"; fi

# --- PATH setup ------------------------------------------------------------
add_to_path() {
  _profile="$1"; _line="export PATH=\"${PREFIX}:\$PATH\""
  if [ -f "$_profile" ]; then
    case "$(cat "$_profile")" in *"$PREFIX"*) return 0 ;; esac
  fi
  printf '\n# Muster Fleet Cloud\n%s\n' "$_line" >> "$_profile"
  info "  Added ${PREFIX} to PATH in ${_profile}"
}

if ! printf '%s' "$PATH" | tr ':' '\n' | grep -qx "$PREFIX"; then
  case "$(basename "${SHELL:-/bin/bash}")" in
    zsh)  add_to_path "${HOME}/.zshrc" ;;
    bash) [ -f "${HOME}/.bash_profile" ] \
            && add_to_path "${HOME}/.bash_profile" \
            || add_to_path "${HOME}/.bashrc" ;;
    *)    info "  [note] Add ${PREFIX} to your PATH manually." ;;
  esac
fi

# --- done ------------------------------------------------------------------
info ""
info "Installation complete."
info ""
info "Next steps:"
if [ "$INSTALL_AGENT" -eq 1 ]; then info "  muster-agent --help    # run the fleet agent"; fi
if [ "$INSTALL_TUNNEL" -eq 1 ]; then info "  muster-tunnel --help   # run the tunnel client"; fi
info ""
info "You may need to restart your shell or run:"
info "  export PATH=\"${PREFIX}:\$PATH\""
