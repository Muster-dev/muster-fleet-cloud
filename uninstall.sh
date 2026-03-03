#!/usr/bin/env bash
# Muster Fleet Cloud uninstaller — bash 3.2+
# Usage: bash uninstall.sh
#   or:  curl -fsSL https://raw.githubusercontent.com/Muster-dev/muster-fleet-cloud/main/uninstall.sh | bash
set -euo pipefail

die() { printf '%b\n' "Error: $1" >&2; exit 1; }
info() { printf '%b\n' "$1"; }

PREFIX="${MUSTER_BIN_DIR:-${HOME}/.local/bin}"

_interactive=false
[ -t 0 ] && _interactive=true
[ -e /dev/tty ] && _interactive=true

# --- find installed binaries -----------------------------------------------
_found=()
for _bin in muster-agent muster-tunnel muster-cloud; do
  if [ -f "${PREFIX}/${_bin}" ]; then
    _found[${#_found[@]}]="${PREFIX}/${_bin}"
  fi
done

if [ ${#_found[@]} -eq 0 ]; then
  info ""
  info "  No fleet cloud binaries found in ${PREFIX}/"
  info "  Nothing to uninstall."
  info ""
  exit 0
fi

# --- show what will be affected --------------------------------------------
info ""
info "  Muster Fleet Cloud — Uninstall"
info ""
info "  Found:"
for _f in "${_found[@]}"; do
  info "    ${_f}"
done
info ""

# --- find shell profile with PATH entry ------------------------------------
_shell_profile=""
for _profile in "${HOME}/.zshrc" "${HOME}/.bashrc" "${HOME}/.bash_profile" "${HOME}/.profile"; do
  if [ -f "$_profile" ] && grep -q "Muster Fleet Cloud" "$_profile" 2>/dev/null; then
    _shell_profile="$_profile"
    break
  fi
done

if [ -n "$_shell_profile" ]; then
  info "  PATH entry in:"
  info "    ${_shell_profile}"
  info ""
fi

# --- ask what to do --------------------------------------------------------
if [ "$_interactive" = true ]; then
  info "  Options:"
  info "    1) Cancel — keep everything"
  if [ -n "$_shell_profile" ]; then
    info "    2) Remove from PATH only — keep binaries"
    info "    3) Remove everything — delete binaries + clean PATH"
  else
    info "    2) Remove everything — delete binaries"
  fi
  info ""
  printf '  Choose [1]: '
  read -r _choice </dev/tty
  _choice="${_choice:-1}"
else
  # Non-interactive: remove everything
  _choice="3"
  [ -z "$_shell_profile" ] && _choice="2"
fi

_clean_path() {
  if [ -n "$_shell_profile" ]; then
    _tmp="${_shell_profile}.fleet-tmp"
    grep -v "# Muster Fleet Cloud" "$_shell_profile" \
      | grep -v "${PREFIX}" > "$_tmp" || true
    mv "$_tmp" "$_shell_profile"
    info "  Cleaned PATH from ${_shell_profile}"
  fi
}

case "$_choice" in
  1)
    info "  Cancelled."
    info ""
    ;;
  2)
    if [ -n "$_shell_profile" ]; then
      # PATH only
      _clean_path
      info ""
      info "  Removed from PATH. Binaries kept in ${PREFIX}/"
      info "  To fully remove, run this again and choose option 3."
    else
      # No PATH entry — this is "remove everything"
      for _f in "${_found[@]}"; do
        rm -f "$_f"
        info "  Removed ${_f}"
      done
      info ""
      info "  Fleet cloud uninstalled."
    fi
    info ""
    ;;
  3)
    for _f in "${_found[@]}"; do
      rm -f "$_f"
      info "  Removed ${_f}"
    done
    _clean_path
    info ""
    info "  Fleet cloud uninstalled."
    info "  To reinstall: curl -fsSL https://raw.githubusercontent.com/Muster-dev/muster-fleet-cloud/main/install.sh | bash"
    info ""
    ;;
  *)
    info "  Invalid choice. Cancelled."
    info ""
    ;;
esac
